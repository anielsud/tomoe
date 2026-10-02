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
