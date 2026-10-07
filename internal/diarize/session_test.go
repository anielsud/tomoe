package diarize

import (
	"sync"
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestLabelNewLocked(t *testing.T) {
	d := &SessionDiarizer{
		lock: &sync.Mutex{},
		timeline: &Timeline{
			Turns:   []session.DiarizeSegment{{Start: 0, End: 10, Speaker: 0}},
			Labels:  map[int]string{0: "Person 1"},
			Through: 10,
		},
		liveTo: map[string]string{"Person 3": "Person 1"},
	}
	for _, tc := range []struct {
		name string
		seg  session.Segment
		want string
	}{
		{"covered by the timeline", session.Segment{Speaker: "Person 9", StartTime: 2, EndTime: 5}, "Person 1"},
		{"mapped live label", session.Segment{Speaker: "Person 3", StartTime: 12, EndTime: 14}, "Person 1"},
		{"unplaced voice", session.Segment{Speaker: "Person 2", StartTime: 12, EndTime: 14}, NewSpeakerLabel},
		{"unplaced voice with a hint", session.Segment{Speaker: "Person 2 (Ana)", StartTime: 12, EndTime: 14}, NewSpeakerLabel + " (Ana)"},
		{"mic", session.Segment{Speaker: "You", Source: "mic", StartTime: 12, EndTime: 14}, "You"},
	} {
		seg := tc.seg
		d.LabelNewLocked(&seg)
		if seg.Speaker != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, seg.Speaker, tc.want)
		}
		if seg.Speaker != tc.seg.Speaker && seg.LiveSpeaker != tc.seg.Speaker {
			t.Errorf("%s: live label not kept (%q)", tc.name, seg.LiveSpeaker)
		}
	}
}

func TestSettleProvisional(t *testing.T) {
	d := &SessionDiarizer{lock: &sync.Mutex{}, sess: &session.Session{Segments: []session.Segment{
		{Speaker: NewSpeakerLabel, LiveSpeaker: "Person 4", StartTime: 10.5, EndTime: 11},
		{Speaker: "Alex?", LiveSpeaker: "Person 4", StartTime: 100, EndTime: 101},
		{Speaker: "Person 1", StartTime: 0, EndTime: 5},
	}}}
	turns := []session.DiarizeSegment{{Start: 0, End: 10, Speaker: 0}}
	d.settleProvisionalLocked(turns, map[int]string{0: "Person 1 (Alex)"})
	if got := d.sess.Segments[0].Speaker; got != "Person 1 (Alex)" {
		t.Errorf("line next to a turn = %q, want its speaker", got)
	}
	if got := d.sess.Segments[1].Speaker; got != "Person 4" {
		t.Errorf("line far from any turn = %q, want its live label", got)
	}
	if got := d.sess.Segments[2].Speaker; got != "Person 1" {
		t.Errorf("a final label changed: %q", got)
	}
}

func TestLabelNewLockedUsesNamedSpeaker(t *testing.T) {
	// A 1:1: the timeline has named the other person, and the live pass
	// mints a new cluster for their voice. The meeting window still shows
	// their name, so the line is theirs, not a provisional new speaker.
	d := &SessionDiarizer{
		lock: &sync.Mutex{},
		timeline: &Timeline{
			Turns:   []session.DiarizeSegment{{Start: 0, End: 10, Speaker: 0}},
			Labels:  map[int]string{0: "Person 1"},
			Through: 10,
		},
		liveTo:  map[string]string{},
		renames: map[int]string{},
	}
	// Read while they spoke earlier (naming timeline speaker 0), and now.
	for _, t := range []float64{1, 3, 5, 7, 9, 13} {
		d.hints = append(d.hints, Hint{T: t + hintLag, Name: "Ana Lopez"})
	}
	seg := session.Segment{Speaker: "Person 7", StartTime: 12, EndTime: 14}
	d.LabelNewLocked(&seg)
	if want := "Person 1 (Ana Lopez)"; seg.Speaker != want {
		t.Errorf("got %q, want %q", seg.Speaker, want)
	}
	// Someone the window names but no speaker has yet stays provisional.
	d.hints[len(d.hints)-1] = Hint{T: 13 + hintLag, Name: "Ben Ito"}
	seg = session.Segment{Speaker: "Person 8", StartTime: 12, EndTime: 14}
	d.LabelNewLocked(&seg)
	if want := "Ben Ito?"; seg.Speaker != want {
		t.Errorf("unknown name: got %q, want %q", seg.Speaker, want)
	}
}
