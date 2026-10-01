package backend

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// A stopped session's late refinements must land in that session, not in
// the one now recording, even though segment IDs repeat across sessions.
// (With another session current, nothing is forwarded to the frontend, so
// this runs without a Wails context.)
func TestEmitSessionSegmentsAppliesLateRefinementsToTheirOwnSession(t *testing.T) {
	a := NewApp()
	stopped := &session.Session{ID: "stopped"}
	current := &session.Session{ID: "current", Segments: []session.Segment{{ID: "seg-1", Text: "current session text"}}}
	a.currentSess = current

	segments := make(chan session.Segment, 4)
	updates := make(chan session.Segment, 4)
	done := a.emitSessionSegments(segments, updates, stopped, nil)

	// The refinement may overtake its segment; both orders must converge.
	updates <- session.Segment{ID: "seg-1", Text: "Refined.", StartTime: 1}
	segments <- session.Segment{ID: "seg-1", Text: "rough", StartTime: 1, Status: "pending"}
	segments <- session.Segment{ID: "seg-2", Text: "Next.", StartTime: 4}
	close(segments)
	close(updates)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done not closed after both channels closed")
	}

	if len(stopped.Segments) != 2 || stopped.Segments[0].Text != "Refined." || stopped.Segments[0].Status != "" {
		t.Errorf("stopped session segments = %+v, want refined seg-1 then seg-2", stopped.Segments)
	}
	if len(current.Segments) != 1 || current.Segments[0].Text != "current session text" {
		t.Errorf("current session was modified: %+v", current.Segments)
	}
}
