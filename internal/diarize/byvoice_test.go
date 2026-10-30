package diarize

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// voiceToward is n fingerprints of one voice, unit length along e.
func voiceToward(n int, e ...float32) voices.Voice {
	var sq float64
	for _, x := range e {
		sq += float64(x * x)
	}
	u := make([]float32, 4)
	for i, x := range e {
		u[i] = x / float32(math.Sqrt(sq))
	}
	var v voices.Voice
	for range n {
		v.Add(u)
	}
	return v
}

func profilesForTest(t *testing.T) *voices.Store {
	t.Helper()
	params := voices.DefaultParams()
	params.MinMeetings = 1
	store, err := voices.Open(filepath.Join(t.TempDir(), "v.json"), SameName, params)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.Learn("earlier", time.Now(), []voices.Learned{
		{Name: "Ilse Novak", Voice: voiceAlong(0, 20)},
		{Name: "Wayde Tyrrel", Voice: voiceAlong(1, 20)},
		{Name: "Bodil Ekdahl", Voice: voiceAlong(2, 20)},
		{Name: "Ines Moro", Voice: voiceToward(20, 0, 0, 0.8, 0.6)},
	})
	return store
}

func TestCallVoicesOverTheWindow(t *testing.T) {
	store := profilesForTest(t)
	vs := map[int]voices.Voice{
		0: voiceAlong(0, 10),               // Ilse's voice; the window says Ilse
		1: voiceAlong(1, 10),               // Wayde's voice; the window says Ilse (a lingering highlight, or Ilse's laptop)
		2: voiceToward(10, 0, 0.6, 0, 0.8), // somewhat like Wayde (0.6); the window says Ines
		3: voiceToward(10, 0, 0, 0, 1),     // like nobody; the window says Ines
		4: voiceAlong(2, 10),               // Bodil's voice; the window named only the room
		5: voiceToward(10, 0, 0.6, 0, 0.8), // somewhat like Wayde; the window says Ulla, who has no profile
		6: voiceAlong(2, 10),               // Bodil's voice; the window says Ulla, who has no profile
	}
	names := map[int]string{0: "Ilse Novak", 1: "Ilse Novak", 2: "Ines Moro", 3: "Ines Moro", 4: "Room - Oslo 4", 5: "Ulla Berg", 6: "Ulla Berg"}
	call := callVoices(store, vs, names, nil)

	if _, ok := call.named[0]; ok {
		t.Errorf("a voice agreeing with the window should leave the window's name: %+v", call.named[0])
	}
	if m := call.named[1]; m.Name != "Wayde Tyrrel" || call.window[1] != "Ilse Novak" {
		t.Errorf("a clear voice should overrule the window, keeping its name: %+v, window %q", m, call.window[1])
	}
	if _, ok := call.named[2]; ok {
		t.Errorf("a weak voice shouldn't overrule the window: %+v", call.named[2])
	}
	if d := call.doubts[2]; d.Name != "Wayde Tyrrel" {
		t.Errorf("a weak disagreeing voice should be a doubt: %+v", call.doubts)
	}
	if _, ok := call.doubts[3]; ok {
		t.Errorf("a voice likest its own profile isn't a doubt: %+v", call.doubts[3])
	}
	if _, ok := call.doubts[5]; ok {
		t.Errorf("without a profile of the window's person a weak voice is nothing to doubt: %+v", call.doubts[5])
	}
	if _, ok := call.named[6]; ok {
		t.Errorf("a voice shouldn't overrule the window's person when they have no profile to be unlike: %+v", call.named[6])
	}
	if d := call.doubts[6]; d.Name != "Bodil Ekdahl" {
		t.Errorf("a clear voice against a window's person with no profile should be a doubt: %+v", call.doubts)
	}
	if m := call.named[4]; m.Name != "Bodil Ekdahl" {
		t.Errorf("a room's name should give way to the voice's: %+v", m)
	}
}

func TestCallVoicesTakenNames(t *testing.T) {
	store := profilesForTest(t)
	vs := map[int]voices.Voice{
		0: voiceAlong(2, 10), // Bodil's voice, no name
		1: voiceAlong(0, 10), // Ilse's voice; the window says Wayde
	}
	names := map[int]string{1: "Wayde Tyrrel"}
	byUser := func(name string) bool { return SameName(name, "Bodil Ekdahl") || SameName(name, "Ilse Novak") }
	call := callVoices(store, vs, names, byUser)
	if _, ok := call.named[0]; ok {
		t.Errorf("an unnamed speaker shouldn't take a name the user gave someone: %+v", call.named[0])
	}
	if m := call.named[1]; m.Name != "Ilse Novak" {
		t.Errorf("the user's name for another speaker shouldn't stop the voice overruling the window (one person split in two): %+v", call.named)
	}
}

func TestUnsureSpeechTeachesNoTile(t *testing.T) {
	sess := &session.Session{
		Segments: []session.Segment{
			{Speaker: "Person 2 (Wayde Tyrrel)", StartTime: 0, EndTime: 10},
			{Speaker: "Person 3 (Bodil Ekdahl)", StartTime: 10, EndTime: 20},
			{Speaker: "Person 4 (Ilse Novak)", StartTime: 20, EndTime: 30},
		},
		VoiceNames:  []session.VoiceName{{Label: "Person 2 (Wayde Tyrrel)", Name: "Wayde Tyrrel", Window: "Ilse Novak"}},
		VoiceDoubts: []session.VoiceDoubt{{Label: "Person 3 (Bodil Ekdahl)", Window: "Bodil Ekdahl", Name: "Wayde Tyrrel"}},
	}
	unsure := unsureAt(sess.Segments, unsureLabels(sess))
	for _, c := range []struct {
		t    float64
		want bool
	}{{5, true}, {15, true}, {25, false}} {
		if got := unsure(c.t); got != c.want {
			t.Errorf("unsure at %v: %v, want %v", c.t, got, c.want)
		}
	}
}
