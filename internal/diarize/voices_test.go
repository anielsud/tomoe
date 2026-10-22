package diarize

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

func voiceAlong(axis, n int) voices.Voice {
	var v voices.Voice
	e := make([]float32, 4)
	e[axis] = 1
	for range n {
		v.Add(e)
	}
	return v
}

func TestSpeakersNamedFromVoiceProfiles(t *testing.T) {
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
	})
	d := &SessionDiarizer{lock: &sync.Mutex{}, opts: SessionOptions{Voices: store}, renames: map[int]string{}}
	d.timeline = &Timeline{
		Turns: []session.DiarizeSegment{
			{Start: 0, End: 10, Speaker: 0},  // the room's tile was lit
			{Start: 10, End: 20, Speaker: 1}, // no name read
			{Start: 20, End: 30, Speaker: 2}, // named by the window
		},
		Labels: map[int]string{0: "Person 1", 1: "Person 2", 2: "Person 3"},
		Voices: map[int]voices.Voice{0: voiceAlong(0, 10), 1: voiceAlong(2, 10), 2: voiceAlong(2, 10)},
	}
	d.hints = []Hint{
		{T: 2.5, Name: "Room - Oslo 4"}, {T: 7.5, Name: "Room - Oslo 4"},
		{T: 22.5, Name: "Bodil Ekdahl"}, {T: 27.5, Name: "Bodil Ekdahl"},
	}
	got := d.labelsLocked(1e9)
	want := map[int]string{
		0: "Person 1 (Ilse Novak)",   // a room's name gives way to the voice's
		1: "Person 2",                // sounds like Bodil, but Bodil is speaker 3
		2: "Person 3 (Bodil Ekdahl)", // the window's name stands
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("speaker %d: %q, want %q", k, got[k], w)
		}
	}
	if vm := d.voiceByLabelLocked()["Person 1 (Ilse Novak)"]; vm == nil || vm.Similarity < 0.99 {
		t.Errorf("a voice-named speaker isn't marked: %+v", vm)
	}
	if vm := d.voiceByLabelLocked()["Person 3 (Bodil Ekdahl)"]; vm != nil {
		t.Errorf("a window-named speaker is marked as named by voice: %+v", vm)
	}
	// The user says speaker 1 is someone else: the profile learns it.
	d.sess = &session.Session{Segments: []session.Segment{{Speaker: "Person 1 (Ilse Novak)", LiveSpeaker: "Person 7", StartTime: 1, EndTime: 9, Source: "monitor"}}}
	d.timeline.Through = 1e9
	if _, ok := d.RenameLocked("Person 1 (Ilse Novak)", "Ilka Varga"); !ok {
		t.Fatal("rename failed")
	}
	if m := d.corrected[0]; m.Name != "Ilse Novak" {
		t.Errorf("the wrong voice name wasn't recorded: %+v", d.corrected)
	}
	if got := d.labelsLocked(1e9); got[0] != "Ilka Varga" {
		t.Errorf("the user's name should beat the voice's: %q", got[0])
	}
	if d.sess.Segments[0].Speaker != "Ilka Varga" || d.sess.Segments[0].VoiceMatch != nil {
		t.Errorf("renamed line: %+v", d.sess.Segments[0])
	}
}

func TestIsRoomTileAndGivenName(t *testing.T) {
	for name, want := range map[string]bool{
		"Room - Oslo - Fjord 2": true,
		"Harbour Zoom Room 1":   true,
		"Unlicensed room":       true,
		"Roomi Sato":            false,
		"Ilse Novak":            false,
	} {
		if IsRoomTile(name) != want {
			t.Errorf("IsRoomTile(%q) = %v", name, !want)
		}
	}
	for label, want := range map[string]string{
		"Ilse Novak":            "Ilse Novak",
		"You":                   "",
		"Person 3":              "",
		"Person 3 (Ilse Novak)": "",
		NewSpeakerLabel:         "",
		"Ilse?":                 "",
		"Room - Oslo 4":         "",
	} {
		if got := GivenName(label); got != want {
			t.Errorf("GivenName(%q) = %q, want %q", label, got, want)
		}
	}
}
