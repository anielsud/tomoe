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

// VoiceMeetingView is one meeting a profile learnt from.
type VoiceMeetingView struct {
	Session      string    `json:"session"`
	Title        string    `json:"title"`
	At           time.Time `json:"at"`
	Source       string    `json:"source"` // voices.FromTile or FromNamed
	Fingerprints int       `json:"fingerprints"`
	// Agreement is how alike this meeting's voice and the rest of the
	// profile's are (0 when it's the only meeting): a meeting far below
	// the others is likely someone else.
	Agreement float64   `json:"agreement"`
	Clip      VoiceClip `json:"clip"`
	// Tiles are a few of the person's tile as the meeting window showed
	// it while their voice was learnt (meetings since tiles were kept).
	Tiles []TileRef `json:"tiles"`
}

// TileRef is a kept tile crop: look Look of session Session.
type TileRef struct {
	Session string `json:"session"`
	Look    int    `json:"look"`
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
	// Meetings, newest first.
	Heard []VoiceMeetingView `json:"heard"`
	// Rejected is how many voices the user said aren't this person.
	Rejected int `json:"rejected"`
	// StartedOver: the user cleared what it learnt; it learns from new
	// meetings.
	StartedOver bool `json:"startedOver"`
	// TakenOut are meetings the user took out of the profile (they can
	// still be moved to the right person).
	TakenOut []VoiceMeetingView `json:"takenOut"`
	// Face is one of the person's tiles, from the latest meeting with one.
	Face *TileRef `json:"face,omitempty"`
	// InUse: the profile names voices (heard in enough meetings, or
	// confirmed).
	InUse     bool `json:"inUse"`
	Confirmed bool `json:"confirmed"`
}

// NamedByVoiceView is a speaker a profile named in a recent meeting.
type NamedByVoiceView struct {
	Session    string    `json:"session"`
	Title      string    `json:"title"`
	At         time.Time `json:"at"`
	Label      string    `json:"label"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	Profile    string    `json:"profile"`
	Similarity float64   `json:"similarity"`
	Seconds    float64   `json:"seconds"`
	Clip       VoiceClip `json:"clip"`
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
	// NamedByVoice are recent speakers the profiles named, to check.
	NamedByVoice []NamedByVoiceView `json:"namedByVoice"`
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
	view := &VoicesView{Enabled: a.config().Meeting.VoiceProfiles, Profiles: []VoiceProfileView{}, Pairs: []VoicePairView{}, Unknown: []UnknownVoice{}, NamedByVoice: []NamedByVoiceView{}}
	titles := a.sessionTitles()
	stores, err := a.voiceStores()
	if err != nil {
		return nil, err
	}
	for model, s := range stores {
		for _, p := range s.Profiles() {
			v := VoiceProfileView{Model: model, ID: p.ID, Name: p.Name, Meetings: len(p.Meetings), Fingerprints: p.Voice().N, LastHeard: p.LastHeard(),
				Heard: []VoiceMeetingView{}, Rejected: len(p.Rejected), StartedOver: len(p.Meetings) == 0 && len(p.Excluded) > 0,
				InUse: s.InUse(&p), Confirmed: p.Confirmed}
			for i, h := range p.Meetings {
				var rest voices.Voice
				for j, o := range p.Meetings {
					if j != i {
						rest.Merge(o.Voice)
					}
				}
				agree := 0.0
				if rest.N > 0 {
					agree = voices.Similarity(h.Voice.Centroid(), rest.Centroid())
				}
				v.Heard = append(v.Heard, VoiceMeetingView{Session: h.Session, Title: titles[h.Session], At: h.At, Source: h.Source, Fingerprints: h.Voice.N, Agreement: agree, Clip: VoiceClip{h.Session, h.Clip.Start, h.Clip.End},
					Tiles: tilesOf(h.Session, append([]string{p.Name}, p.Aliases...), tilesPerMeeting)})
			}
			sort.Slice(v.Heard, func(i, j int) bool { return v.Heard[i].At.After(v.Heard[j].At) })
			v.TakenOut = []VoiceMeetingView{}
			for _, sid := range p.Excluded {
				v.TakenOut = append(v.TakenOut, VoiceMeetingView{Session: sid, Title: titles[sid], Tiles: tilesOf(sid, append([]string{p.Name}, p.Aliases...), tilesPerMeeting)})
			}
			for _, h := range v.Heard {
				if len(h.Tiles) > 0 {
					v.Face = &h.Tiles[0]
					break
				}
			}
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
	view.Unknown, view.NamedByVoice = a.recentVoices(stores)
	return view, nil
}

// sessionTitles maps saved sessions' IDs to their titles.
func (a *App) sessionTitles() map[string]string {
	out := map[string]string{}
	if list, err := a.store.List(); err == nil {
		for _, s := range list {
			out[s.ID] = s.Title
		}
	}
	return out
}

// recentVoices are, from recent saved meetings: the speakers with no name
// who spoke for a while, each with the profile it's closest to if that's
// close enough to suggest; and the speakers profiles named.
func (a *App) recentVoices(stores map[string]*voices.Store) ([]UnknownVoice, []NamedByVoiceView) {
	named := []NamedByVoiceView{}
	list, err := a.store.List()
	if err != nil {
		return nil, named
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
		byVoice := map[string]session.VoiceName{}
		for _, vn := range full.VoiceNames {
			byVoice[vn.Label] = vn
		}
		for _, seg := range full.Segments {
			_, voiceNamed := byVoice[seg.Speaker]
			if !isUnnamed(seg.Speaker) && !voiceNamed {
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
			start := sp.longest.StartTime
			if vn, ok := byVoice[label]; ok {
				named = append(named, NamedByVoiceView{Session: sess.ID, Title: full.Title, At: full.CreatedAt, Label: label, Name: vn.Name, Model: vn.Model, Profile: vn.Profile,
					Similarity: vn.Similarity, Seconds: sp.seconds, Clip: VoiceClip{sess.ID, start, min(sp.longest.EndTime, start+clipSeconds)}})
				continue
			}
			if sp.seconds < unknownSeconds {
				continue
			}
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
	sort.SliceStable(named, func(i, j int) bool {
		if !named[i].At.Equal(named[j].At) {
			return named[i].At.After(named[j].At)
		}
		return named[i].Seconds > named[j].Seconds
	})
	return out, named
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

// unforgetVoice lets a name the user just gave be learnt again, if it
// was forgotten.
func (a *App) unforgetVoice(name string) {
	stores, err := a.voiceStores()
	if err != nil {
		return
	}
	for _, s := range stores {
		_ = s.Unforget(name)
	}
}

// correctVoiceName, when the user renames a speaker a profile named in a
// saved meeting to someone else, records that the voice isn't the
// profile's person, and drops the voice name from the session (the
// caller saves it).
func (a *App) correctVoiceName(sess *session.Session, label, name string) {
	kept := sess.VoiceNames[:0]
	for _, vn := range sess.VoiceNames {
		if vn.Label != label {
			kept = append(kept, vn)
			continue
		}
		if !diarize.SameName(vn.Name, name) {
			if err := a.rejectVoice(sess, vn, label); err != nil {
				fmt.Printf("session %s: couldn't record a wrong voice name: %v\n", sess.ID, err)
			}
		}
	}
	sess.VoiceNames = kept
}

// rejectVoice records that the voice labeled label in sess isn't vn's
// profile's person. Call before relabeling the lines.
func (a *App) rejectVoice(sess *session.Session, vn session.VoiceName, label string) error {
	p, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil {
		return err
	}
	learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(l string) string {
		if l == label {
			return l
		}
		return ""
	}))
	if len(learned) == 0 {
		return fmt.Errorf("no fingerprints for %q", label)
	}
	store, err := a.voiceStore(vn.Model)
	if err != nil {
		return err
	}
	return store.Reject(vn.Profile, sess.ID, sess.CreatedAt, learned[0].Voice)
}

// personLabel is the anonymous part of a label: "Person 3" of
// "Person 3 (Ana)".
var personLabel = regexp.MustCompile(`^Person \d+`)

// NotThem says a speaker a profile named in a saved meeting isn't that
// person: the profile learns it, and the lines lose the name.
func (a *App) NotThem(sessionID, label string) error {
	a.fixSignals()
	a.mu.Lock()
	recording := a.currentSess != nil && a.currentSess.ID == sessionID
	a.mu.Unlock()
	if recording {
		return fmt.Errorf("this meeting is still recording: name the speaker in the transcript instead")
	}
	sess, err := a.store.Load(sessionID)
	if err != nil {
		return err
	}
	var vn *session.VoiceName
	for i := range sess.VoiceNames {
		if sess.VoiceNames[i].Label == label {
			vn = &sess.VoiceNames[i]
		}
	}
	if vn == nil {
		return fmt.Errorf("%q wasn't named by voice", label)
	}
	if err := a.rejectVoice(sess, *vn, label); err != nil {
		return err
	}
	anon := personLabel.FindString(label)
	if anon == "" {
		anon = label
	}
	for i := range sess.Segments {
		if seg := &sess.Segments[i]; seg.Speaker == label {
			seg.Speaker, seg.VoiceMatch = anon, nil
		}
	}
	kept := sess.VoiceNames[:0]
	for _, x := range sess.VoiceNames {
		if x.Label != label {
			kept = append(kept, x)
		}
	}
	sess.VoiceNames = kept
	return a.store.Save(sess)
}

// RemoveVoiceMeeting takes a meeting out of a profile: what it taught is
// dropped and it's never learnt from again.
func (a *App) RemoveVoiceMeeting(model, id, sessionID string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.ForgetMeeting(id, sessionID)
}

// StartVoiceOver clears what a profile learnt, keeping its name; it learns
// from new meetings only.
func (a *App) StartVoiceOver(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.StartOver(id)
}

// ConfirmVoice says a profile is the person's voice: it names voices
// before it's been heard in enough meetings.
func (a *App) ConfirmVoice(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Confirm(id)
}

// tilesPerMeeting is how many of a person's tiles a profile shows per
// meeting.
const tilesPerMeeting = 2

type sessionTile struct {
	look int
	name string
}

// Kept tiles by session, read from looks.jsonl (cached while it's
// unchanged).
var (
	tilesMu    sync.Mutex
	tilesCache = map[string]tilesEntry{}
)

type tilesEntry struct {
	mod   time.Time
	tiles []sessionTile
}

// tilesOf picks up to n of a session's kept tiles named any of names,
// spread over the meeting.
func tilesOf(sessionID string, names []string, n int) []TileRef {
	dir := filepath.Join(config.SessionDir(), sessionID)
	st, err := os.Stat(filepath.Join(dir, "looks.jsonl"))
	if err != nil {
		return []TileRef{}
	}
	tilesMu.Lock()
	e, ok := tilesCache[dir]
	if !ok || !e.mod.Equal(st.ModTime()) {
		e = tilesEntry{mod: st.ModTime()}
		looks, _ := videohint.ReadLooks(dir)
		for _, l := range looks {
			if l.HasTile {
				e.tiles = append(e.tiles, sessionTile{l.ID, l.Name})
			}
		}
		tilesCache[dir] = e
	}
	tilesMu.Unlock()
	var match []sessionTile
	for _, t := range e.tiles {
		for _, n := range names {
			if diarize.SameName(t.name, n) {
				match = append(match, t)
				break
			}
		}
	}
	out := []TileRef{}
	for i := 0; i < n && i < len(match); i++ {
		// First, then spread evenly to the last.
		j := 0
		if n > 1 {
			j = i * (len(match) - 1) / (n - 1)
		}
		out = append(out, TileRef{sessionID, match[j].look})
	}
	return out
}

// GetLookTile returns a kept tile crop as a JPEG data URL.
func (a *App) GetLookTile(sessionID string, look int) (string, error) {
	b, err := videohint.ReadTile(filepath.Join(config.SessionDir(), filepath.Base(sessionID)), look)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// MoveVoiceMeeting says what a profile learnt from a meeting was someone
// else's: it teaches toName's profile instead (see voices.Store.Move). A
// meeting already taken out is worked out again from the saved meeting.
func (a *App) MoveVoiceMeeting(model, id, sessionID, toName string) error {
	a.fixSignals()
	if toName == "" {
		return fmt.Errorf("a name is needed")
	}
	store, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	var prof *voices.Profile
	for _, p := range store.Profiles() {
		if p.ID == id {
			prof = &p
			break
		}
	}
	if prof == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	sess, err := a.store.Load(sessionID)
	if err != nil {
		return err
	}
	for _, h := range prof.Meetings {
		if h.Session == sessionID {
			return store.Move(id, sessionID, sess.CreatedAt, h.Voice, toName)
		}
	}
	dir := filepath.Join(config.SessionDir(), sessionID)
	p, info, err := loadSavedCached(dir)
	if err != nil {
		return err
	}
	names := append([]string{prof.Name}, prof.Aliases...)
	for _, l := range diarize.VoicesFromTiles(p, savedHints(dir, sess), info.Offset) {
		for _, n := range names {
			if diarize.SameName(l.Name, n) {
				return store.Move(id, sessionID, sess.CreatedAt, l.Voice, toName)
			}
		}
	}
	return fmt.Errorf("no voice under %s's tile in that meeting", prof.Name)
}

// DiscardedVoiceClip is a clip of the voice a profile learnt from a
// meeting it no longer learns from, worked out again from the meeting.
func (a *App) DiscardedVoiceClip(model, id, sessionID string) (VoiceClip, error) {
	a.fixSignals()
	store, err := a.voiceStore(model)
	if err != nil {
		return VoiceClip{}, err
	}
	var names []string
	for _, p := range store.Profiles() {
		if p.ID == id {
			names = append([]string{p.Name}, p.Aliases...)
		}
	}
	sess, err := a.store.Load(sessionID)
	if err != nil {
		return VoiceClip{}, err
	}
	dir := filepath.Join(config.SessionDir(), sessionID)
	p, info, err := loadSavedCached(dir)
	if err != nil {
		return VoiceClip{}, err
	}
	for _, l := range diarize.VoicesFromTiles(p, savedHints(dir, sess), info.Offset) {
		for _, n := range names {
			if diarize.SameName(l.Name, n) {
				return VoiceClip{sessionID, l.Clip.Start, l.Clip.End}, nil
			}
		}
	}
	return VoiceClip{}, fmt.Errorf("no voice under that tile in the meeting")
}
