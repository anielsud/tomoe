// Package voices keeps voice profiles: what a named person's voice
// fingerprint looks like, learnt across meetings, so a voice the meeting
// window can't name (in a meeting room, on the phone, in a call with no
// window to read) can still be named by who it sounds like.
//
// A profile is learnt only from strong evidence: fingerprints heard while
// the person's own tile was lit, and names the user gave a speaker. Each
// meeting a profile was heard in is kept separately, so learning a meeting
// again replaces what it taught, and a meeting can be forgotten. Profiles
// are local (DataDir/voices/<speaker model>.json) and only fingerprints are stored, never
// audio; a profile's clips point into the meeting's own recording.
//
// On saved meetings (docs/speaker-attribution-research.md, "Voice
// profiles"): a voice compared to profiles learnt from the other meetings
// was named in 112 of 119 cases at the default bar, all right, and voices
// with no profile were wrongly named in 1 of 57.
package voices

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Voice is a set of fingerprints: their sum (each one L2-normalized) and
// how many. Its direction is the voice.
type Voice struct {
	Sum []float32 `json:"sum"`
	N   int       `json:"n"`
}

// Add adds a fingerprint.
func (v *Voice) Add(emb []float32) {
	if v.Sum == nil {
		v.Sum = make([]float32, len(emb))
	}
	if len(emb) != len(v.Sum) {
		return
	}
	for i, x := range emb {
		v.Sum[i] += x
	}
	v.N++
}

// Merge adds another voice's fingerprints.
func (v *Voice) Merge(o Voice) {
	if o.N == 0 {
		return
	}
	if v.Sum == nil {
		v.Sum = make([]float32, len(o.Sum))
	}
	if len(o.Sum) != len(v.Sum) {
		return
	}
	for i, x := range o.Sum {
		v.Sum[i] += x
	}
	v.N += o.N
}

// Centroid is the voice's direction, L2-normalized (nil if empty).
func (v Voice) Centroid() []float32 {
	var s float64
	for _, x := range v.Sum {
		s += float64(x) * float64(x)
	}
	if v.N == 0 || s == 0 {
		return nil
	}
	s = math.Sqrt(s)
	out := make([]float32, len(v.Sum))
	for i, x := range v.Sum {
		out[i] = float32(float64(x) / s)
	}
	return out
}

// Similarity is the cosine similarity of two centroids (-2 if either is
// missing or they differ in size: another speaker model's).
func Similarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -2
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Clip is a stretch of a meeting's recording to play, in session time
// (seconds).
type Clip struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Source is where a meeting's fingerprints for a profile came from.
const (
	FromTile  = "tile"  // the person's own tile was lit
	FromNamed = "named" // the user named the speaker
)

// Heard is what one meeting taught a profile.
type Heard struct {
	Session string    `json:"session"`
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Voice   Voice     `json:"voice"`
	Clip    Clip      `json:"clip"`
}

// Profile is one person's voice.
type Profile struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Meetings []Heard   `json:"meetings"`
	Created  time.Time `json:"created"`
	// Apart lists profiles the user said are someone else, so they're
	// never offered as the same person again.
	Apart []string `json:"apart,omitempty"`
	// Aliases are names of profiles merged into this one: learning under
	// them teaches this profile.
	Aliases []string `json:"aliases,omitempty"`

	centroid []float32
	n        int
}

// Voice is all the profile's meetings together.
func (p *Profile) Voice() Voice {
	var v Voice
	for _, h := range p.Meetings {
		v.Merge(h.Voice)
	}
	return v
}

// LastHeard is the most recent meeting the profile was heard in.
func (p *Profile) LastHeard() time.Time {
	var t time.Time
	for _, h := range p.Meetings {
		if h.At.After(t) {
			t = h.At
		}
	}
	return t
}

func (p *Profile) refresh() {
	v := p.Voice()
	p.centroid, p.n = v.Centroid(), v.N
}

// Params are the rules for naming a voice from the profiles.
type Params struct {
	// Threshold is the least similarity to a profile that names a voice.
	Threshold float64
	// Margin is how far the best profile must beat the next.
	Margin float64
	// MinFingerprints is the least a voice, and a profile, must have.
	MinFingerprints int
	// LearnFloor: a meeting's voice that's less like the profile of the
	// same name than this (once the profile has LearnFloorAfter
	// fingerprints) isn't learnt: a misread name or a ring on the wrong
	// tile, not a new side of the voice.
	LearnFloor      float64
	LearnFloorAfter int
}

// DefaultParams are measured on saved meetings (see the package comment).
func DefaultParams() Params {
	return Params{Threshold: 0.7, Margin: 0.1, MinFingerprints: 8, LearnFloor: 0.5, LearnFloorAfter: 30}
}

// Store is the profiles on disk. Safe for concurrent use.
type Store struct {
	path   string
	same   func(a, b string) bool
	params Params

	mu        sync.Mutex
	profiles  []*Profile
	forgotten []string
	// modTime is the file's when last read or written: another process
	// (the CLI's learn, the app) may have changed it since.
	modTime time.Time
}

// Path is where a speaker model's profiles are kept.
func Path(dataDir, model string) string { return filepath.Join(dataDir, "voices", model+".json") }

// Open loads the profiles at path (none if it doesn't exist). same says
// whether two names are one person's (a tile's name cut short, a misread
// letter).
func Open(path string, same func(a, b string) bool, params Params) (*Store, error) {
	s := &Store{path: path, same: same, params: params}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetParams changes the naming rules.
func (s *Store) SetParams(p Params) {
	s.mu.Lock()
	s.params = p
	s.mu.Unlock()
}

func (s *Store) load() error {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.profiles, s.modTime = nil, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("voice profiles %s: %w", s.path, err)
	}
	for _, p := range f.Profiles {
		p.refresh()
	}
	s.profiles, s.forgotten, s.modTime = f.Profiles, f.Forgotten, st.ModTime()
	return nil
}

// syncLocked rereads the file if another process changed it. A file that
// can't be read keeps what's in memory.
func (s *Store) syncLocked() {
	st, err := os.Stat(s.path)
	if err == nil && !st.ModTime().Equal(s.modTime) {
		_ = s.load()
	}
}

type file struct {
	Version  int        `json:"version"`
	Profiles []*Profile `json:"profiles"`
	// Forgotten are names the user forgot, never learnt again.
	Forgotten []string `json:"forgotten,omitempty"`
}

// saveLocked writes the profiles atomically.
func (s *Store) saveLocked() error {
	b, err := json.Marshal(file{Version: 1, Profiles: s.profiles, Forgotten: s.forgotten})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.modTime = st.ModTime()
	}
	return nil
}

// Profiles returns a copy of every profile, most recently heard first.
func (s *Store) Profiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		c := *p
		c.Meetings = append([]Heard(nil), p.Meetings...)
		c.Apart = append([]string(nil), p.Apart...)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastHeard().After(out[j].LastHeard()) })
	return out
}

// Learned is one name's voice in a meeting, to learn.
type Learned struct {
	Name   string
	Source string
	Voice  Voice
	Clip   Clip
}

// Learn records what a meeting taught: each name's voice replaces what
// that meeting taught the name's profile before (a new profile if no name
// matches). A voice unlike its profile is skipped (Params.LearnFloor).
// Returns the names learnt.
func (s *Store) Learn(sessionID string, at time.Time, learned []Learned) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	var done []string
	for _, l := range learned {
		if l.Name == "" || l.Voice.N < s.params.MinFingerprints || s.forgottenLocked(l.Name) {
			continue
		}
		p := s.findLocked(l.Name)
		if p == nil {
			p = &Profile{ID: newID(), Name: l.Name, Created: time.Now()}
			s.profiles = append(s.profiles, p)
		}
		var others Voice
		var kept []Heard
		for _, h := range p.Meetings {
			if h.Session != sessionID {
				kept = append(kept, h)
				others.Merge(h.Voice)
			}
		}
		if others.N >= s.params.LearnFloorAfter && Similarity(others.Centroid(), l.Voice.Centroid()) < s.params.LearnFloor {
			continue
		}
		p.Meetings = append(kept, Heard{Session: sessionID, At: at, Source: l.Source, Voice: l.Voice, Clip: l.Clip})
		p.refresh()
		done = append(done, p.Name)
	}
	if len(done) == 0 {
		return nil, nil
	}
	return done, s.saveLocked()
}

// findLocked is the profile named name (or once named it), if any.
func (s *Store) findLocked(name string) *Profile {
	for _, p := range s.profiles {
		if p.Name == name || contains(p.Aliases, name) {
			return p
		}
	}
	for _, p := range s.profiles {
		if s.sameLocked(p.Name, name) {
			return p
		}
		for _, a := range p.Aliases {
			if s.sameLocked(a, name) {
				return p
			}
		}
	}
	return nil
}

func (s *Store) sameLocked(a, b string) bool {
	return a == b || (s.same != nil && s.same(a, b))
}

func (s *Store) forgottenLocked(name string) bool {
	for _, f := range s.forgotten {
		if s.sameLocked(f, name) {
			return true
		}
	}
	return false
}

// Match names voices (by key) from the profiles: each voice gets the
// profile it's most like, if that's at least Threshold and beats the
// next by Margin, one voice per profile (the likest wins), and never a
// name taken says is already someone else's in this meeting.
func (s *Store) Match(voices map[int]Voice, taken func(name string) bool) map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	type cand struct {
		key int
		p   *Profile
		sim float64
	}
	var cands []cand
	for key, v := range voices {
		if v.N < s.params.MinFingerprints {
			continue
		}
		c := v.Centroid()
		best, second := cand{key: key, sim: -2}, -2.0
		for _, p := range s.profiles {
			if p.n < s.params.MinFingerprints {
				continue
			}
			sim := Similarity(c, p.centroid)
			if sim > best.sim {
				second, best = best.sim, cand{key, p, sim}
			} else if sim > second {
				second = sim
			}
		}
		if best.p != nil && best.sim >= s.params.Threshold && best.sim-second >= s.params.Margin {
			cands = append(cands, best)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	out := map[int]string{}
	used := map[string]bool{}
	for _, c := range cands {
		if used[c.p.ID] || (taken != nil && taken(c.p.Name)) {
			continue
		}
		used[c.p.ID] = true
		out[c.key] = c.p.Name
	}
	return out
}

// Rename renames a profile.
func (s *Store) Rename(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	if p.Name != name {
		p.Aliases = appendOnce(p.Aliases, p.Name)
		p.Name = name
	}
	return s.saveLocked()
}

// Merge folds profile from into into: one person under two names.
func (s *Store) Merge(into, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	a, b := s.byIDLocked(into), s.byIDLocked(from)
	if a == nil || b == nil || a == b {
		return fmt.Errorf("can't merge voice profiles %q and %q", into, from)
	}
	// A meeting in both keeps both: they were two voices then.
	a.Meetings = append(a.Meetings, b.Meetings...)
	a.Aliases = appendOnce(a.Aliases, b.Name)
	for _, n := range b.Aliases {
		a.Aliases = appendOnce(a.Aliases, n)
	}
	a.refresh()
	s.removeLocked(from)
	return s.saveLocked()
}

// Apart records that two profiles are different people.
func (s *Store) Apart(a, b string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	pa, pb := s.byIDLocked(a), s.byIDLocked(b)
	if pa == nil || pb == nil {
		return fmt.Errorf("no such voice profiles")
	}
	pa.Apart = appendOnce(pa.Apart, b)
	pb.Apart = appendOnce(pb.Apart, a)
	return s.saveLocked()
}

// Forget deletes a profile, and its names are never learnt again.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	s.forgotten = appendOnce(s.forgotten, p.Name)
	for _, a := range p.Aliases {
		s.forgotten = appendOnce(s.forgotten, a)
	}
	s.removeLocked(id)
	return s.saveLocked()
}

// ForgetMeeting drops what one meeting taught a profile (a profile left
// with no meetings is deleted).
func (s *Store) ForgetMeeting(id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	kept := p.Meetings[:0]
	for _, h := range p.Meetings {
		if h.Session != sessionID {
			kept = append(kept, h)
		}
	}
	p.Meetings = kept
	if len(kept) == 0 {
		s.removeLocked(id)
	} else {
		p.refresh()
	}
	return s.saveLocked()
}

// Pair is two profiles that may be one person.
type Pair struct {
	A, B       string // profile IDs
	Similarity float64
}

// LookAlike lists pairs of profiles whose voices are at least min alike
// and that the user hasn't said are different people, likest first: the
// same person under two names, or a profile learnt from a misread name.
func (s *Store) LookAlike(min float64) []Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	var out []Pair
	for i, a := range s.profiles {
		for _, b := range s.profiles[i+1:] {
			if contains(a.Apart, b.ID) {
				continue
			}
			if sim := Similarity(a.centroid, b.centroid); sim >= min {
				out = append(out, Pair{a.ID, b.ID, sim})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// Closest is the profile most like v and how alike (nil if none).
func (s *Store) Closest(v Voice) (*Profile, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	c := v.Centroid()
	var best *Profile
	bestSim := -2.0
	for _, p := range s.profiles {
		if sim := Similarity(c, p.centroid); sim > bestSim {
			best, bestSim = p, sim
		}
	}
	if best == nil {
		return nil, 0
	}
	cp := *best
	return &cp, bestSim
}

func (s *Store) byIDLocked(id string) *Profile {
	for _, p := range s.profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Store) removeLocked(id string) {
	kept := s.profiles[:0]
	for _, p := range s.profiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	s.profiles = kept
	for _, p := range s.profiles {
		kept := p.Apart[:0]
		for _, a := range p.Apart {
			if a != id {
				kept = append(kept, a)
			}
		}
		p.Apart = kept
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func appendOnce(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
