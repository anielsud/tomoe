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
	s, err := Open(path, sameFold, DefaultParams())
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
	if got[1] != "Ilse Novak" || got[3] != "Wayde Tyrrel" || got[2] != "" || got[4] != "" {
		t.Errorf("Match = %v", got)
	}

	// Reopened from disk, it's the same.
	s2, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.2))}, nil); got[1] != "Ilse Novak" {
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
	if got[2] != "Ilse Novak" || got[1] != "" {
		t.Errorf("one voice per profile, the likest: %v", got)
	}
	got = s.Match(map[int]Voice{2: voiceOf(10, unit(0, 1, 0.1))}, func(n string) bool { return n == "Ilse Novak" })
	if got[2] != "" {
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
	if got := s.Match(map[int]Voice{1: v}, nil); got[1] != "" {
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
	if err := s.ForgetMeeting(ids["Wayde Tyrrel"], "m1"); err != nil {
		t.Fatal(err)
	}
	if ps := s.Profiles(); len(ps) != 1 || ps[0].Name != "Ilse M. Novak" || len(ps[0].Apart) != 0 {
		t.Errorf("after forgetting Wayde's only meeting: %+v", ps)
	}
	if err := s.Forget(ids["Ilse Novak"]); err != nil || len(s.Profiles()) != 0 {
		t.Errorf("Forget: %v, %d left", err, len(s.Profiles()))
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
