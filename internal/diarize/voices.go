package diarize

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// learnShare is how much of a fingerprint's speech must be under one name
// for it to teach that name's profile: stricter than for clustering
// (roomTileShare), as a profile outlives the meeting.
const learnShare = 0.8

// VoicesFromTiles is each person's voice in a meeting, from the
// fingerprints heard while their own tile was lit (not a room's: a room's
// tile is lit for everyone in it). offset converts the fingerprints' time
// to session time (StreamInfo.Offset).
func VoicesFromTiles(p *Prepared, hints []Hint, offset float64) []voices.Learned {
	at := TileTimeline(hints, hintLag)
	display := tileDisplayNames(hints)
	return voicesBy(p, offset, func(t float64) string {
		k := at(t)
		if k == "" || IsRoomTile(k) {
			return ""
		}
		return display[k]
	}, voices.FromTile)
}

// VoicesFromSpeakers is each named speaker's voice in a meeting: who(t)
// is the name of whoever spoke at session time t, or "".
func VoicesFromSpeakers(p *Prepared, offset float64, who func(t float64) string) []voices.Learned {
	return voicesBy(p, offset, who, voices.FromNamed)
}

// SpeakerAt returns who spoke at session time t by the transcript: the
// name of the line covering t, if name gives one for its label.
func SpeakerAt(segs []session.Segment, name func(label string) string) func(t float64) string {
	type span struct {
		start, end float64
		name       string
	}
	var spans []span
	for _, s := range segs {
		if n := name(s.Speaker); n != "" && s.EndTime > s.StartTime {
			spans = append(spans, span{s.StartTime, s.EndTime, n})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return func(t float64) string {
		i := sort.Search(len(spans), func(i int) bool { return spans[i].start > t }) - 1
		for ; i >= 0; i-- {
			if t <= spans[i].end {
				return spans[i].name
			}
			if t-spans[i].start > 60 {
				break
			}
		}
		return ""
	}
}

func voicesBy(p *Prepared, offset float64, who func(t float64) string, source string) []voices.Learned {
	names := p.PairTiles(who, offset, learnShare)
	by := map[string][]int{}
	for i, n := range names {
		if n != "" {
			by[n] = append(by[n], i)
		}
	}
	out := make([]voices.Learned, 0, len(by))
	for name, idx := range by {
		var v voices.Voice
		for _, i := range idx {
			v.Add(p.Embeddings[i])
		}
		// The clip: of the fingerprints with a few seconds of speech, the
		// likest the voice as a whole; else the one with the most speech.
		// (The likest of all can be a word or two in a long gap.)
		c := v.Centroid()
		best, bestSim, mostSpeech, most := -1, -2.0, idx[0], 0.0
		for _, i := range idx {
			_, _, spoke := p.pairSpan(i)
			if spoke > most {
				mostSpeech, most = i, spoke
			}
			if sim := voices.Similarity(c, p.Embeddings[i]); spoke >= clipSpeech && sim > bestSim {
				best, bestSim = i, sim
			}
		}
		if best < 0 {
			best = mostSpeech
		}
		start, end, _ := p.pairSpan(best)
		out = append(out, voices.Learned{Name: name, Source: source, Voice: v, Clip: voices.Clip{Start: start + offset, End: end + offset}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// clipSpeech is how much speech (seconds) a profile's clip should have.
const clipSpeech = 4.0

// pairSpan is when embedding i's speaker talks in its window (seconds
// into the audio): from its first active frame to its last, and how many
// seconds of that they're talking.
func (p *Prepared) pairSpan(i int) (float64, float64, float64) {
	m := p.Meta
	pair := p.Pairs[i]
	if m.SampleRate == 0 || m.ReceptiveFieldShift == 0 || pair.Chunk >= len(p.Labels) {
		return 0, 0, 0
	}
	start := int(float32(pair.Chunk)*float32(m.WindowShift)/float32(m.ReceptiveFieldShift) + 0.5)
	first, last, active := -1, -1, 0
	for f, row := range p.Labels[pair.Chunk] {
		if pair.Speaker < len(row) && row[pair.Speaker] != 0 {
			if first < 0 {
				first = f
			}
			last = f
			active++
		}
	}
	if first < 0 {
		return 0, 0, 0
	}
	sec := float64(m.ReceptiveFieldShift) / float64(m.SampleRate)
	return float64(start+first) * sec, float64(start+last+1) * sec, float64(active) * sec
}

// tileDisplayNames maps each tile key TileTimeline gives to the name as
// the tile shows it.
func tileDisplayNames(hints []Hint) map[string]string {
	var all []string
	for _, h := range hints {
		all = append(all, h.Name)
		all = append(all, h.Candidates...)
	}
	canon := canonicalNames(all)
	out := map[string]string{}
	for _, c := range canon {
		if k := normalizeName(c); k != "" {
			out[k] = c
		}
	}
	return out
}

var (
	voiceStoreMu sync.Mutex
	voiceStores  = map[string]*voices.Store{}
)

// VoiceStore is the process's voice profiles for a speaker model
// (DataDir/voices/<model>.json: another model's fingerprints can't be
// compared), with m's naming rules; one Store per model per process, so
// the app's edits and a meeting's learning go through the same one.
func VoiceStore(m config.MeetingConfig, model string) (*voices.Store, error) {
	if model == "" {
		return nil, fmt.Errorf("no speaker model named")
	}
	voiceStoreMu.Lock()
	defer voiceStoreMu.Unlock()
	params := voices.DefaultParams()
	if m.VoiceProfileThreshold > 0 {
		params.Threshold = m.VoiceProfileThreshold
	}
	if m.VoiceProfileMargin > 0 {
		params.Margin = m.VoiceProfileMargin
	}
	if s := voiceStores[model]; s != nil {
		s.SetParams(params)
		return s, nil
	}
	s, err := voices.Open(voices.Path(config.DataDir(), model), SameName, params)
	if err != nil {
		return nil, err
	}
	voiceStores[model] = s
	return s, nil
}

// GivenName is the name in a label the user gave a speaker ("" for the
// app's own labels: "You", "Person 3", "Person 3 (Ana)", "New speaker",
// provisional "Ana?", or a room).
func GivenName(label string) string {
	l := strings.TrimSpace(label)
	switch {
	case l == "", l == "You", strings.HasPrefix(l, "Person "), isProvisional(l), IsRoomTile(l):
		return ""
	}
	return l
}

// LoadSaved reads a saved session's fingerprints (diarization.gob) and how
// they were made (diarization.json).
func LoadSaved(dir string) (*Prepared, StreamInfo, error) {
	var info StreamInfo
	f, err := os.Open(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	var p Prepared
	if err := gob.NewDecoder(f).Decode(&p); err != nil {
		return nil, info, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "diarization.json"))
	if err != nil {
		return nil, info, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, info, err
	}
	if info.Model == "" {
		info.Model = modelBySettings(info.Params.Threshold)
	}
	return &p, info, nil
}

// modelBySettings is the speaker model whose clustering cut a meeting was
// diarized with, for fingerprints saved before the model was recorded;
// "" if none or several match.
func modelBySettings(threshold float64) string {
	found := ""
	for _, m := range models.SpeakerModels {
		if m.StreamThreshold == threshold {
			if found != "" {
				return ""
			}
			found = m.ID
		}
	}
	return found
}

// LearnSaved teaches the voice profiles from a saved session: the voices
// heard while a person's own tile was lit, and the speakers given a name
// (GivenName). hints are the session's name reads.
func LearnSaved(m config.MeetingConfig, dir string, sess *session.Session, hints []Hint) ([]string, error) {
	p, info, err := LoadSaved(dir)
	if err != nil {
		return nil, err
	}
	if info.Model == "" {
		return nil, fmt.Errorf("can't tell which speaker model made the fingerprints")
	}
	store, err := VoiceStore(m, info.Model)
	if err != nil {
		return nil, err
	}
	learned := VoicesFromTiles(p, hints, info.Offset)
	learned = append(learned, VoicesFromSpeakers(p, info.Offset, SpeakerAt(sess.Segments, GivenName))...)
	return store.Learn(sess.ID, sess.CreatedAt, learned)
}
