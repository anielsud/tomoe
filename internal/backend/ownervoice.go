package backend

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// The user's own voice (config mic_voices) tells them from others on
// their microphone. It starts from their mic track in recent meetings
// (ownerSeed) and keeps learning from utterances that are surely theirs.

// ownerMinPrints is how many fingerprints the voice needs before it's
// used; ownerKeep caps how many it weighs (older ones fade), so it
// follows a new microphone or a cold.
const (
	ownerMinPrints = 20
	ownerKeep      = 2000
	ownerSaveEvery = 10
)

type ownerVoice struct {
	mu      sync.Mutex
	path    string
	v       voices.Voice
	unsaved int
}

var (
	ownersMu sync.Mutex
	owners   = map[string]*ownerVoice{}
)

// ownerFor is the user's voice for a speaker model, loaded once.
func ownerFor(modelID string) *ownerVoice {
	path := voices.OwnerPath(config.DataDir(), modelID)
	ownersMu.Lock()
	defer ownersMu.Unlock()
	if o, ok := owners[path]; ok {
		return o
	}
	o := &ownerVoice{path: path}
	if v, err := voices.LoadOwner(path); err == nil {
		o.v = v
	} else {
		fmt.Printf("owner voice: %v\n", err)
	}
	owners[path] = o
	return o
}

// centroid is the voice to compare with, nil until it has enough.
func (o *ownerVoice) centroid() []float32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.v.N < ownerMinPrints {
		return nil
	}
	return o.v.Centroid()
}

// heard adds a fingerprint that is surely the user's (L2-normalized).
func (o *ownerVoice) heard(emb []float32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.v.Add(emb)
	if o.v.N > ownerKeep {
		for i := range o.v.Sum {
			o.v.Sum[i] /= 2
		}
		o.v.N /= 2
	}
	if o.unsaved++; o.unsaved >= ownerSaveEvery {
		o.unsaved = 0
		if err := voices.SaveOwner(o.path, o.v); err != nil {
			fmt.Printf("owner voice: %v\n", err)
		}
	}
}

// ownerSeed builds the user's voice from their mic track in recent
// meetings when there's none yet: up to 12 utterances of 2.5 s or more
// from each of the last 20 meetings with one, then only those close to
// the first estimate (the mic can carry someone else, or the room).
// Runs in the background with its own embedder.
func ownerSeed(modelID, modelPath string) {
	o := ownerFor(modelID)
	o.mu.Lock()
	have := o.v.N
	o.mu.Unlock()
	if have >= ownerMinPrints {
		return
	}
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return
	}
	emb, err := speaker.NewEmbedder(modelPath)
	if err != nil {
		return
	}
	defer emb.Close()
	dir := config.SessionDir()
	ents, _ := os.ReadDir(dir)
	type sess struct {
		dir string
		at  int64
	}
	var ss []sess
	for _, e := range ents {
		if fi, err := os.Stat(filepath.Join(dir, e.Name(), "audio.m4a")); err == nil {
			ss = append(ss, sess{filepath.Join(dir, e.Name()), fi.ModTime().Unix()})
		}
	}
	sort.Slice(ss, func(a, b int) bool { return ss[a].at > ss[b].at })
	var prints [][]float32
	used := 0
	for _, s := range ss {
		if used >= 20 {
			break
		}
		var rec struct {
			Segments []struct {
				StartTime float64 `json:"start_time"`
				EndTime   float64 `json:"end_time"`
				Source    string  `json:"source"`
			} `json:"segments"`
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "session.json"))
		if err != nil || json.Unmarshal(b, &rec) != nil {
			continue
		}
		n := 0
		for _, g := range rec.Segments {
			if n >= 12 || g.Source != "mic" || g.EndTime-g.StartTime < 2.5 {
				continue
			}
			// The mic is the second track of a meeting with a mic and a
			// meeting side (session.SaveAudioM4A).
			x := micTrack(ffmpeg, filepath.Join(s.dir, "audio.m4a"), g.StartTime, min(g.EndTime-g.StartTime, 8))
			if len(x) < 16000 {
				continue
			}
			if e, err := emb.Extract(x); err == nil {
				if p := voices.Normalized(e); p != nil {
					prints = append(prints, p)
					n++
				}
			}
		}
		if n > 0 {
			used++
		}
	}
	var first voices.Voice
	for _, p := range prints {
		first.Add(p)
	}
	c := first.Centroid()
	var v voices.Voice
	for _, p := range prints {
		if voices.Similarity(c, p) >= 0.5 {
			v.Add(p)
		}
	}
	if v.N < ownerMinPrints {
		fmt.Printf("owner voice: only %d fingerprints from %d meetings; mic stays \"You\" until more are heard\n", v.N, used)
		return
	}
	o.mu.Lock()
	o.v.Merge(v)
	err = voices.SaveOwner(o.path, o.v)
	o.mu.Unlock()
	fmt.Printf("owner voice: %d fingerprints from %d meetings (%v)\n", v.N, used, err)
}

// micTrack decodes a stretch of a saved meeting's mic track as 16 kHz
// samples (nil if the meeting has no separate mic track).
func micTrack(ffmpeg, path string, start, dur float64) []float32 {
	cmd := exec.Command(ffmpeg, "-v", "error", "-ss", fmt.Sprintf("%.2f", start), "-t", fmt.Sprintf("%.2f", dur),
		"-i", path, "-map", "0:a:1", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return nil
	}
	b := out.Bytes()
	f := make([]float32, len(b)/2)
	for i := range f {
		f[i] = float32(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
	}
	return f
}
