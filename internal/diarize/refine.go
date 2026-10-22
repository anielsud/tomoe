package diarize

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// Refining speaker cuts. Where two people talk back to back, a line is cut
// where the diarization says the voice changed. That change comes from the
// segmentation model, which places it a second or two off when the two
// voices come through one microphone (a meeting room): every window
// covering the change agrees, so the error isn't averaged away. Short
// fingerprints across the change find it: on 9 changes the owner marked by
// ear, median error 0.98 s -> 0.12 s (docs/speaker-attribution-research.md,
// "Refining speaker cuts").

// RefineParams are how cuts are refined.
type RefineParams struct {
	// Window is each short fingerprint's length, Hop the step between
	// them, Span how far either side of the cut is searched (seconds).
	Window, Hop, Span float64
	// MaxMove is the most speech moved across a cut (seconds): beyond it
	// the two lines' voices are too mixed to trust a move.
	MaxMove float64
	// MaxAlike skips cuts between voices this alike or more (cosine of the
	// two speakers' voices in the meeting): too close to tell apart in a
	// second of audio.
	MaxAlike float64
	// MinGain is how much better the new split must explain the voices
	// than the current cut (summed difference in likeness, per second
	// searched) for the cut to move: a cut that's already right stays.
	MinGain float64
	// From and To, if To > 0, limit refining to cuts in that stretch
	// (session seconds).
	From, To float64
}

// refineDebug logs each cut's decision (TOMOE_REFINE_DEBUG).
var refineDebug = os.Getenv("TOMOE_REFINE_DEBUG") != ""

// DefaultRefineParams were measured on marked changes (see above).
func DefaultRefineParams() RefineParams {
	return RefineParams{Window: 1.0, Hop: 0.25, Span: 2.0, MaxMove: 2.0, MaxAlike: 0.8, MinGain: 0.4}
}

// RefineCuts moves the cut between each pair of back-to-back lines with
// different speakers in a saved session (dir) to where the voice changes,
// moving the words on the wrong side to the other line. embedderPath is the
// speaker model the session was diarized with. Runs at low priority.
// Returns how many cuts moved; the caller saves sess.
func RefineCuts(dir string, sess *session.Session, embedderPath string, rp RefineParams) (int, error) {
	runtime.LockOSThread()
	defer releaseThread()
	lowerThreadPriority()

	p, info, err := LoadSaved(dir)
	if err != nil {
		return 0, err
	}
	segs := sess.Segments
	order := make([]int, 0, len(segs))
	for i := range segs {
		if session.Diarizable(segs[i]) && len(segs[i].Words) > 0 {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool { return segs[order[a]].StartTime < segs[order[b]].StartTime })
	byLabel := map[string]voices.Voice{}
	for _, l := range VoicesFromSpeakers(p, info.Offset, SpeakerAt(segs, func(label string) string { return label })) {
		byLabel[l.Name] = l.Voice
	}
	emb, err := speaker.NewEmbedder(embedderPath)
	if err != nil {
		return 0, err
	}
	defer emb.Close()
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return 0, err
	}
	audioPath := filepath.Join(dir, "audio.m4a")
	moved := 0
	prev := -1 // the last line still holding words
	for _, cur := range order {
		if prev < 0 {
			prev = cur
			continue
		}
		a, b := &segs[prev], &segs[cur]
		prev = cur
		if a.Speaker == b.Speaker || b.StartTime-a.EndTime > 0.05 {
			continue
		}
		va, vb := byLabel[a.Speaker], byLabel[b.Speaker]
		alike := voices.Similarity(va.Centroid(), vb.Centroid())
		if va.N < 8 || vb.N < 8 || alike >= rp.MaxAlike {
			if refineDebug {
				fmt.Printf("refine %.2f: skipped (fingerprints %d/%d, alike %.2f)\n", b.StartTime, va.N, vb.N, alike)
			}
			continue
		}
		cut := b.StartTime
		if rp.To > 0 && (cut < rp.From || cut > rp.To) {
			continue
		}
		t0 := cut - rp.Span - rp.Window/2
		audio := meetingTrack(ffmpeg, audioPath, t0, 2*rp.Span+rp.Window)
		if audio == nil {
			continue
		}
		at, ok := changeAt(emb, audio, t0, cut, va, vb, rp)
		// A change at the edge of the search is no change found: the
		// voices near the cut aren't these two (people trading turns).
		edge := math.Abs(at-cut) >= rp.Span-rp.Hop
		n := 0
		if ok && !edge {
			n = moveWords(a, b, at, rp.MaxMove)
			if len(b.Words) == 0 {
				prev = indexOf(a, segs) // b went whole into a
			}
		}
		if refineDebug {
			fmt.Printf("refine %.2f: change at %+.2f, moved %d words\n", cut, at-cut, n)
		}
		if n == 0 {
			continue
		}
		moved++
	}
	// Drop the lines whose words all went to a neighbour.
	kept := sess.Segments[:0]
	for _, s := range sess.Segments {
		if !session.Diarizable(s) || len(s.Words) > 0 || s.Text != "" {
			kept = append(kept, s)
		}
	}
	sess.Segments = kept
	return moved, nil
}

func indexOf(s *session.Segment, segs []session.Segment) int {
	for i := range segs {
		if &segs[i] == s {
			return i
		}
	}
	return -1
}

// meetingTrack decodes the meeting's side of the recording (the M4A's
// last audio track: mix, mic, meeting; or its only one) from t0, n seconds.
func meetingTrack(ffmpeg, path string, t0, n float64) []float32 {
	for _, track := range []string{"0:a:2", "0:a:0"} {
		cmd := exec.Command(ffmpeg, "-v", "error", "-ss", fmt.Sprintf("%.3f", max(0, t0)), "-t", fmt.Sprintf("%.3f", n),
			"-i", path, "-map", track, "-ac", "1", "-ar", "16000", "-f", "f32le", "pipe:1")
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil && out.Len() > 0 {
			s := make([]float32, out.Len()/4)
			if binary.Read(bytes.NewReader(out.Bytes()), binary.LittleEndian, s) == nil {
				return s
			}
		}
	}
	return nil
}

// changeAt finds where voice a gives way to voice b near cut, from short
// fingerprints of audio (which starts at session time t0): the split with
// the most of a's voice before it and of b's after.
func changeAt(emb *speaker.Embedder, audio []float32, t0, cut float64, a, b voices.Voice, rp RefineParams) (float64, bool) {
	ca, cb := a.Centroid(), b.Centroid()
	var ts, ss []float64
	for c := cut - rp.Span; c <= cut+rp.Span+1e-9; c += rp.Hop {
		i0 := int((c - rp.Window/2 - t0) * 16000)
		i1 := i0 + int(rp.Window*16000)
		if i0 < 0 || i1 > len(audio) {
			continue
		}
		e, err := emb.Extract(audio[i0:i1])
		if err != nil {
			continue
		}
		var n float64
		for _, x := range e {
			n += float64(x) * float64(x)
		}
		if n == 0 {
			continue
		}
		n = math.Sqrt(n)
		for i := range e {
			e[i] = float32(float64(e[i]) / n)
		}
		ts = append(ts, c)
		ss = append(ss, voices.Similarity(e, cb)-voices.Similarity(e, ca))
	}
	if len(ts) < 3 {
		return 0, false
	}
	// score(k): a before ts[k], b from it on. Start with all b, then move
	// each step into a.
	score := 0.0
	for _, s := range ss {
		score += s
	}
	best, bestScore := ts[0]-rp.Hop/2, score
	atCut := math.Inf(-1)
	for k, s := range ss {
		if ts[k] >= cut && atCut == math.Inf(-1) {
			atCut = score
		}
		score -= 2 * s
		if score > bestScore {
			bestScore = score
			best = ts[k] + rp.Hop/2
		}
	}
	if atCut == math.Inf(-1) {
		atCut = score
	}
	if (bestScore-atCut)*rp.Hop < rp.MinGain {
		return cut, false
	}
	return best, true
}

// moveWords moves the words on the wrong side of at between back-to-back
// lines a and b (a's words starting at or after at go to b; b's starting
// before at go to a), at most maxMove seconds of speech. A line that
// short can go whole (a fragment between two cuts is the neighbour's):
// it's left with no words or text. Returns how many words moved.
func moveWords(a, b *session.Segment, at, maxMove float64) int {
	mid := func(w session.Word) float64 { return w.Start + min(0.1, (w.End-w.Start)/2) }
	n := 0
	if at < b.StartTime {
		k := len(a.Words)
		for k > 0 && mid(a.Words[k-1]) >= at {
			k--
		}
		if k == len(a.Words) || a.Words[len(a.Words)-1].End-a.Words[k].Start > maxMove {
			return 0
		}
		n = len(a.Words) - k
		b.Words = append(append([]session.Word(nil), a.Words[k:]...), b.Words...)
		a.Words = a.Words[:k]
	} else {
		k := 0
		for k < len(b.Words) && mid(b.Words[k]) < at {
			k++
		}
		if k == 0 || b.Words[k-1].End-b.Words[0].Start > maxMove {
			return 0
		}
		n = k
		a.Words = append(append([]session.Word(nil), a.Words...), b.Words[:k]...)
		b.Words = append([]session.Word(nil), b.Words[k:]...)
	}
	for _, s := range []*session.Segment{a, b} {
		if len(s.Words) == 0 {
			s.Text = "" // absorbed whole by its neighbour: dropped by the caller
			continue
		}
		parts := make([]string, len(s.Words))
		for i, w := range s.Words {
			parts[i] = w.Text
		}
		s.Text = strings.Join(parts, " ")
		s.StartTime, s.EndTime = s.Words[0].Start, s.Words[len(s.Words)-1].End
	}
	return n
}

// RefineSavedCuts is RefineCuts on a saved session, with the speaker model
// its fingerprints were made with.
func RefineSavedCuts(dir string, sess *session.Session, rp RefineParams) (int, error) {
	_, info, err := LoadSaved(dir)
	if err != nil {
		return 0, err
	}
	m, ok := models.SpeakerModelByID(info.Model)
	if !ok {
		return 0, fmt.Errorf("can't tell which speaker model made the fingerprints")
	}
	return RefineCuts(dir, sess, filepath.Join(config.ModelDir(), m.File), rp)
}
