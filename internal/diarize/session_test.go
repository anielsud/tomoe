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
