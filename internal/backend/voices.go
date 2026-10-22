package backend

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// The Voices page manages the voice profiles (MeetingConfig.VoiceProfiles):
// the people Tomoe knows by voice, pairs of profiles that may be one
// person, and voices from recent meetings nobody has named yet.

// VoiceClip is a stretch of a meeting to play.
type VoiceClip struct {
	Session string  `json:"session"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
}

// VoiceProfileView is one profile on the Voices page.
type VoiceProfileView struct {
	Model        string    `json:"model"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Meetings     int       `json:"meetings"`
	Fingerprints int       `json:"fingerprints"`
	FromTiles    int       `json:"fromTiles"` // meetings learnt from their own tile
	FromNamed    int       `json:"fromNamed"` // meetings where the user named them
	LastHeard    time.Time `json:"lastHeard"`
	Clip         VoiceClip `json:"clip"`
}

// VoicePairView is two profiles that sound alike: one person?
type VoicePairView struct {
	Model      string  `json:"model"`
	A          string  `json:"a"`
	B          string  `json:"b"`
	Similarity float64 `json:"similarity"`
}

// UnknownVoice is a speaker in a recent meeting with no name: who's this?
type UnknownVoice struct {
	Session string    `json:"session"`
	Title   string    `json:"title"`
	At      time.Time `json:"at"`
	Label   string    `json:"label"`
	Seconds float64   `json:"seconds"`
	Clip    VoiceClip `json:"clip"`
	// SoundsLike is the closest profile, if close enough to suggest.
	SoundsLike string  `json:"soundsLike,omitempty"`
	Similarity float64 `json:"similarity,omitempty"`
}

// VoicesView is the Voices page.
type VoicesView struct {
	Enabled  bool               `json:"enabled"`
	Profiles []VoiceProfileView `json:"profiles"`
	Pairs    []VoicePairView    `json:"pairs"`
	Unknown  []UnknownVoice     `json:"unknown"`
}

const (
	// unknownDays and unknownSeconds pick the voices worth asking about:
	// from meetings this recent, speaking this long.
	unknownDays    = 14
	unknownSeconds = 30
	// suggestAbove is how alike a nameless voice and a profile must be to
	// suggest it ("sounds like"): below the naming bar, as the user
	// decides.
	suggestAbove = 0.5
	// clipSeconds caps a clip's length.
	clipSeconds = 8
)

// unnamedLabel is a label with no name: "Person 3", or one named only by a
// room's tile.
var unnamedLabel = regexp.MustCompile(`^Person \d+$`)

func isUnnamed(label string) bool {
	if unnamedLabel.MatchString(label) {
		return true
	}
	name := session.HintName(label)
	return name != "" && diarize.IsRoomTile(name)
}

// voiceStores are the profile stores that exist, one per speaker model.
func (a *App) voiceStores() (map[string]*voices.Store, error) {
	cfg := a.config()
	out := map[string]*voices.Store{}
	for _, m := range models.SpeakerModels {
		if _, err := os.Stat(voices.Path(config.DataDir(), m.ID)); err != nil {
			continue
		}
		s, err := diarize.VoiceStore(cfg.Meeting, m.ID)
		if err != nil {
			return nil, err
		}
		out[m.ID] = s
	}
	return out, nil
}

func (a *App) config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return *config.DefaultConfig()
	}
	return *a.cfg
}

// Voices is the Voices page: every profile, likest pairs, and nameless
// voices from recent meetings.
func (a *App) Voices() (*VoicesView, error) {
	a.fixSignals()
	view := &VoicesView{Enabled: a.config().Meeting.VoiceProfiles, Profiles: []VoiceProfileView{}, Pairs: []VoicePairView{}, Unknown: []UnknownVoice{}}
	stores, err := a.voiceStores()
	if err != nil {
		return nil, err
	}
	for model, s := range stores {
		for _, p := range s.Profiles() {
			v := VoiceProfileView{Model: model, ID: p.ID, Name: p.Name, Meetings: len(p.Meetings), Fingerprints: p.Voice().N, LastHeard: p.LastHeard()}
			var latest time.Time
			for _, h := range p.Meetings {
				switch h.Source {
				case voices.FromTile:
					v.FromTiles++
				case voices.FromNamed:
					v.FromNamed++
				}
				if h.At.After(latest) && h.Clip.End > h.Clip.Start {
					latest, v.Clip = h.At, VoiceClip{h.Session, h.Clip.Start, h.Clip.End}
				}
			}
			view.Profiles = append(view.Profiles, v)
		}
		for _, pr := range s.LookAlike(voices.DefaultParams().Threshold) {
			view.Pairs = append(view.Pairs, VoicePairView{model, pr.A, pr.B, pr.Similarity})
		}
	}
	sort.SliceStable(view.Profiles, func(i, j int) bool { return view.Profiles[i].LastHeard.After(view.Profiles[j].LastHeard) })
	sort.SliceStable(view.Pairs, func(i, j int) bool { return view.Pairs[i].Similarity > view.Pairs[j].Similarity })
	view.Unknown = a.unknownVoices(stores)
	return view, nil
}

// unknownVoices are the speakers of recent saved meetings with no name who
// spoke for a while, the longest first, each with the profile it's
// closest to if that's close enough to suggest.
func (a *App) unknownVoices(stores map[string]*voices.Store) []UnknownVoice {
	list, err := a.store.List()
	if err != nil {
		return nil
	}
	since := time.Now().AddDate(0, 0, -unknownDays)
	out := []UnknownVoice{}
	for _, sess := range list {
		if sess.CreatedAt.Before(since) {
			continue
		}
		full, err := a.store.Load(sess.ID)
		if err != nil {
			continue
		}
		type spoke struct {
			seconds float64
			longest session.Segment
		}
		by := map[string]*spoke{}
		for _, seg := range full.Segments {
			if !isUnnamed(seg.Speaker) {
				continue
			}
			sp := by[seg.Speaker]
			if sp == nil {
				sp = &spoke{}
				by[seg.Speaker] = sp
			}
			d := seg.EndTime - seg.StartTime
			sp.seconds += d
			if d > sp.longest.EndTime-sp.longest.StartTime {
				sp.longest = seg
			}
		}
		var cands []UnknownVoice
		for label, sp := range by {
			if sp.seconds < unknownSeconds {
				continue
			}
			start := sp.longest.StartTime
			cands = append(cands, UnknownVoice{
				Session: sess.ID, Title: full.Title, At: full.CreatedAt, Label: label, Seconds: sp.seconds,
				Clip: VoiceClip{sess.ID, start, min(sp.longest.EndTime, start+clipSeconds)},
			})
		}
		if len(cands) == 0 {
			continue
		}
		a.suggest(full, cands, stores)
		out = append(out, cands...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Seconds > out[j].Seconds
	})
	return out
}

// suggest fills in the profile each nameless voice sounds most like, from
// the meeting's fingerprints.
func (a *App) suggest(sess *session.Session, cands []UnknownVoice, stores map[string]*voices.Store) {
	p, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil || stores[info.Model] == nil {
		return
	}
	for i := range cands {
		label := cands[i].Label
		learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(l string) string {
			if l == label {
				return l
			}
			return ""
		}))
		if len(learned) == 0 {
			continue
		}
		if prof, sim := stores[info.Model].Closest(learned[0].Voice); prof != nil && sim >= suggestAbove {
			cands[i].SoundsLike, cands[i].Similarity = prof.Name, sim
		}
	}
}

// Fingerprints are megabytes a meeting; the Voices page reads the same
// few each time it's opened.
var (
	savedMu    sync.Mutex
	savedCache = map[string]savedEntry{}
)

type savedEntry struct {
	mod  time.Time
	p    *diarize.Prepared
	info diarize.StreamInfo
}

func loadSavedCached(dir string) (*diarize.Prepared, diarize.StreamInfo, error) {
	st, err := os.Stat(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil, diarize.StreamInfo{}, err
	}
	savedMu.Lock()
	defer savedMu.Unlock()
	if e, ok := savedCache[dir]; ok && e.mod.Equal(st.ModTime()) {
		return e.p, e.info, nil
	}
	p, info, err := diarize.LoadSaved(dir)
	if err != nil {
		return nil, info, err
	}
	savedCache[dir] = savedEntry{st.ModTime(), p, info}
	return p, info, nil
}

func (a *App) voiceStore(model string) (*voices.Store, error) {
	if model == "" {
		return nil, fmt.Errorf("no speaker model given")
	}
	return diarize.VoiceStore(a.config().Meeting, model)
}

// RenameVoice renames a profile.
func (a *App) RenameVoice(model, id, name string) error {
	if name == "" {
		return fmt.Errorf("a name is needed")
	}
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Rename(id, name)
}

// MergeVoices makes two profiles one person, under into's name.
func (a *App) MergeVoices(model, into, from string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Merge(into, from)
}

// KeepVoicesApart records that two profiles are different people.
func (a *App) KeepVoicesApart(model, x, y string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Apart(x, y)
}

// ForgetVoice deletes a profile.
func (a *App) ForgetVoice(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Forget(id)
}

// LearnVoicesFromSavedSessions teaches the profiles from every saved
// meeting with fingerprints (as `tomoe voices learn` does), returning how
// many people it learnt.
func (a *App) LearnVoicesFromSavedSessions() (int, error) {
	a.fixSignals()
	list, err := a.store.List()
	if err != nil {
		return 0, err
	}
	m := a.config().Meeting
	people := map[string]bool{}
	for _, s := range list {
		full, err := a.store.Load(s.ID)
		if err != nil {
			continue
		}
		dir := filepath.Join(config.SessionDir(), s.ID)
		names, err := diarize.LearnSaved(m, dir, full, savedHints(dir, full))
		if err != nil {
			continue
		}
		for _, n := range names {
			people[n] = true
		}
	}
	return len(people), nil
}

// learnNamedLater teaches the profiles from a saved meeting after the
// user named a speaker in it, if profiles are on.
func (a *App) learnNamedLater(sess *session.Session) {
	if !a.config().Meeting.VoiceProfiles {
		return
	}
	go func() {
		dir := filepath.Join(config.SessionDir(), sess.ID)
		if _, err := diarize.LearnSaved(a.config().Meeting, dir, sess, savedHints(dir, sess)); err != nil {
			fmt.Printf("session %s: voice profiles not updated: %v\n", sess.ID, err)
		}
	}()
}

// savedHints are a saved session's name reads from the meeting window.
func savedHints(dir string, sess *session.Session) []diarize.Hint {
	looks, _ := videohint.ReadLooks(dir)
	var hints []diarize.Hint
	for _, l := range looks {
		t := l.Time.Sub(sess.CreatedAt).Seconds()
		if name, candidates := l.Accepted(); name != "" {
			hints = append(hints, diarize.Hint{T: t, Name: name})
		} else if len(candidates) > 1 {
			hints = append(hints, diarize.Hint{T: t, Candidates: candidates})
		}
	}
	return hints
}

// PlayVoiceClip returns a stretch of a meeting's audio (the meeting's
// side, not the mic) as a WAV data URL to play.
func (a *App) PlayVoiceClip(clip VoiceClip) (string, error) {
	a.fixSignals()
	if clip.End <= clip.Start {
		return "", fmt.Errorf("no clip")
	}
	path := filepath.Join(config.SessionDir(), filepath.Base(clip.Session), "audio.m4a")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("this meeting's audio isn't saved")
	}
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return "", err
	}
	dur := min(clip.End-clip.Start, clipSeconds)
	// A meeting with a mic has three tracks: the mix, the mic, the meeting
	// (session.SaveAudioM4A); one with a single source has just it.
	for _, track := range []string{"0:a:2", "0:a:0"} {
		cmd := exec.Command(ffmpeg, "-v", "error", "-ss", fmt.Sprintf("%.2f", clip.Start), "-t", fmt.Sprintf("%.2f", dur),
			"-i", path, "-map", track, "-ac", "1", "-ar", "16000", "-f", "wav", "pipe:1")
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil && out.Len() > 44 {
			return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
		}
	}
	return "", fmt.Errorf("couldn't cut the clip")
}
