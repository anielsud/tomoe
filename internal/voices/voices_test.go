package voices

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unit is a 4-dimensional fingerprint pointing mostly along axis i, a
// little along j.
func unit(i, j int, tilt float64) []float32 {
	v := make([]float32, 4)
	v[i] = float32(math.Sqrt(1 - tilt*tilt))
	v[j] += float32(tilt)
	return v
}

func voiceOf(n int, emb []float32) Voice {
	var v Voice
	for range n {
		v.Add(emb)
	}
	return v
}

func sameFold(a, b string) bool { return strings.EqualFold(a, b) }

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "voices", "m.json")
	// One meeting is enough here; TestMinMeetings covers the rule.
	params := DefaultParams()
	params.MinMeetings = 1
	s, err := Open(path, sameFold, params)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestLearnAndMatch(t *testing.T) {
	s, path := open(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.Learn("m1", at, []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.1))},
		{Name: "Wayde Tyrrel", Source: FromTile, Voice: voiceOf(20, unit(1, 0, 0.1))},
		{Name: "Too Short", Source: FromTile, Voice: voiceOf(3, unit(2, 0, 0))},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Profiles()); n != 2 {
		t.Fatalf("%d profiles, want 2 (a voice under MinFingerprints isn't learnt)", n)
	}
	got := s.Match(map[int]Voice{
		1: voiceOf(10, unit(0, 1, 0.2)),  // Ilse
		2: voiceOf(10, unit(2, 3, 0.1)),  // nobody
		3: voiceOf(10, unit(1, 0, 0.25)), // Wayde
		4: voiceOf(3, unit(0, 1, 0.1)),   // Ilse, but too few fingerprints
	}, nil)
	if got[1].Name != "Ilse Novak" || got[3].Name != "Wayde Tyrrel" || got[2].Name != "" || got[4].Name != "" {
		t.Errorf("Match = %v", got)
	}

	// Reopened from disk, it's the same.
	s2, err := Open(path, sameFold, s.params)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.2))}, nil); got[1].Name != "Ilse Novak" {
		t.Errorf("after reopening: %v", got)
	}
}

func TestMatchOneVoicePerProfileAndTaken(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	got := s.Match(map[int]Voice{
		1: voiceOf(10, unit(0, 1, 0.3)),
		2: voiceOf(10, unit(0, 1, 0.1)), // likelier
	}, nil)
	if got[2].Name != "Ilse Novak" || got[1].Name != "" || got[2].Similarity < 0.99 {
		t.Errorf("one voice per profile, the likest: %v", got)
	}
	got = s.Match(map[int]Voice{2: voiceOf(10, unit(0, 1, 0.1))}, func(n string) bool { return n == "Ilse Novak" })
	if got[2].Name != "" {
		t.Errorf("a name already someone else's in the meeting was given: %v", got)
	}
}

func TestMatchNeedsMargin(t *testing.T) {
	s, _ := open(t)
	// Two profiles about equally like the voice: neither is named.
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.3))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0.3))},
	})
	v := voiceOf(10, []float32{0.7071, 0.7071, 0, 0})
	if got := s.Match(map[int]Voice{1: v}, nil); got[1].Name != "" {
		t.Errorf("named without a margin: %v", got)
	}
}

func TestLearnReplacesTheMeetingAndSkipsUnlikeVoices(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(40, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "ilse novak", Voice: voiceOf(10, unit(0, 1, 0.1))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(12, unit(0, 1, 0.1))}})
	ps := s.Profiles()
	if len(ps) != 1 || len(ps[0].Meetings) != 2 || ps[0].Voice().N != 52 {
		t.Fatalf("relearning a meeting should replace it: %+v", ps)
	}
	// A misread name: the voice is nothing like the profile's.
	names, _ := s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(10, unit(3, 2, 0))}})
	if len(names) != 0 || len(s.Profiles()[0].Meetings) != 2 {
		t.Errorf("an unlike voice was learnt: %v", names)
	}
}

func TestMergeApartForget(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.05))},
		{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0.1))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0))},
	})
	ids := map[string]string{}
	for _, p := range s.Profiles() {
		ids[p.Name] = p.ID
	}
	pairs := s.LookAlike(0.7)
	if len(pairs) != 1 {
		t.Fatalf("LookAlike = %v, want the two Ilse profiles", pairs)
	}
	if err := s.Apart(pairs[0].A, pairs[0].B); err != nil {
		t.Fatal(err)
	}
	if len(s.LookAlike(0.7)) != 0 {
		t.Error("a pair said to be different people was offered again")
	}
	if err := s.Merge(ids["Ilse Novak"], ids["I. Novak (guest)"]); err != nil {
		t.Fatal(err)
	}
	if ps := s.Profiles(); len(ps) != 2 {
		t.Fatalf("after merging: %d profiles", len(ps))
	}
	if err := s.Rename(ids["Ilse Novak"], "Ilse M. Novak"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ids["Wayde Tyrrel"]); err != nil {
		t.Fatal(err)
	}
	if ps := s.Profiles(); len(ps) != 1 || ps[0].Name != "Ilse M. Novak" || len(ps[0].Apart) != 0 {
		t.Errorf("after forgetting Wayde: %+v", ps)
	}
	// Learning under a merged-away or old name teaches the same profile.
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0.05))}})
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.05))}})
	if ps := s.Profiles(); len(ps) != 1 || len(ps[0].Meetings) != 4 {
		t.Errorf("old names should teach the profile they're part of: %+v", ps)
	}
	if err := s.Forget(ids["Ilse Novak"]); err != nil || len(s.Profiles()) != 0 {
		t.Errorf("Forget: %v, %d left", err, len(s.Profiles()))
	}
	// A forgotten person isn't learnt again, under any of their names.
	_, _ = s.Learn("m4", time.Now(), []Learned{
		{Name: "Ilse M. Novak", Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0))},
	})
	if n := len(s.Profiles()); n != 0 {
		t.Errorf("a forgotten profile came back: %d", n)
	}
}

func TestStoreSeesAnotherProcesssChanges(t *testing.T) {
	s, path := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	other, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = other.Learn("m2", time.Now(), []Learned{{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0))}})
	// Make sure the modification time differs from what s last saw.
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	if n := len(s.Profiles()); n != 2 {
		t.Errorf("the other process's profile wasn't seen: %d profiles", n)
	}
}

func TestRejectStartOverAndMeetings(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.1))}})
	id := s.Profiles()[0].ID
	stranger := voiceOf(10, unit(0, 2, 0.4)) // 0.92 like Ilse: named her
	if got := s.Match(map[int]Voice{1: stranger}, nil); got[1].Name != "Ilse Novak" {
		t.Fatalf("setup: %v", got)
	}
	if err := s.Reject(id, "m9", time.Now(), stranger); err != nil {
		t.Fatal(err)
	}
	if got := s.Match(map[int]Voice{1: stranger}, nil); got[1].Name != "" {
		t.Errorf("a rejected voice was named again: %v", got)
	}
	if got := s.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.05))}, nil); got[1].Name != "Ilse Novak" {
		t.Errorf("Ilse herself stopped being named: %v", got)
	}

	// Taking a meeting out: it's never learnt from again.
	if err := s.ForgetMeeting(id, "m2"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.1))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 1 || p.Meetings[0].Session != "m1" {
		t.Errorf("a removed meeting came back: %+v", p.Meetings)
	}

	// Starting over: no meetings, the old ones stay out, new ones teach.
	if err := s.StartOver(id); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 0 {
		t.Errorf("an old meeting was learnt after starting over: %+v", p.Meetings)
	}
	if got := s.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0))}, nil); got[1].Name != "" {
		t.Errorf("a started-over profile with nothing learnt named a voice: %v", got)
	}
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 1 || p.Meetings[0].Session != "m3" {
		t.Errorf("a new meeting wasn't learnt after starting over: %+v", p.Meetings)
	}
}

func TestUnforget(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	_ = s.Forget(s.Profiles()[0].ID)
	if err := s.Unforget("ilse novak"); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}}); len(names) != 1 {
		t.Errorf("a name the user gave again wasn't learnt: %v", names)
	}
}

func TestMinMeetings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "v.json"), sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	ilse := voiceOf(10, unit(0, 1, 0))
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "Wayde Tyrrel", Source: FromNamed, Voice: voiceOf(20, unit(1, 0, 0))},
		{Name: "Bodil Ekdahl", Source: FromTile, Voice: voiceOf(20, unit(2, 0, 0))},
	})
	got := s.Match(map[int]Voice{1: ilse, 2: voiceOf(10, unit(1, 0, 0)), 3: voiceOf(10, unit(2, 0, 0))}, nil)
	if got[1].Name != "" || got[2].Name != "Wayde Tyrrel" || got[3].Name != "" {
		t.Errorf("after one meeting only a person the user named should name voices: %v", got)
	}
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.05))}})
	var bodil string
	for _, p := range s.Profiles() {
		if p.Name == "Bodil Ekdahl" {
			bodil = p.ID
		}
	}
	_ = s.Confirm(bodil)
	got = s.Match(map[int]Voice{1: ilse, 3: voiceOf(10, unit(2, 0, 0))}, nil)
	if got[1].Name != "Ilse Novak" || got[3].Name != "Bodil Ekdahl" {
		t.Errorf("heard twice, or confirmed, should name voices: %v", got)
	}
}
