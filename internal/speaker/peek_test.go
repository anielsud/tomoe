package speaker

import (
	"testing"
	"time"
)

func TestTrackerPeekDoesNotChangeClusters(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	if got := tracker.Peek([]float32{1, 0, 0, 0}); got != "Person 1" {
		t.Errorf("Peek on empty tracker = %q, want %q", got, "Person 1")
	}
	if tracker.NumSpeakers() != 0 {
		t.Fatalf("Peek created a speaker")
	}

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	if got := tracker.Peek([]float32{0.99, 0.1, 0, 0}); got != "Person 1" {
		t.Errorf("Peek on a confident match = %q, want %q", got, "Person 1")
	}

	// A different voice moments later: provisionally the recent speaker,
	// once the grace window has passed, the next new speaker.
	clock.advance(time.Second)
	if got := tracker.Peek([]float32{0, 1, 0, 0}); got != "Person 1" {
		t.Errorf("Peek within the grace window = %q, want %q", got, "Person 1")
	}
	clock.advance(DefaultTuning().StickyGraceWindow)
	if got := tracker.Peek([]float32{0, 1, 0, 0}); got != "Person 2" {
		t.Errorf("Peek after the grace window = %q, want %q", got, "Person 2")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d after Peeks, want 1", tracker.NumSpeakers())
	}
}

func TestTrackerPeekRespectsStickyThresholdMarginDisabled(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tuning := tracker.Tuning()
	tuning.StickyThresholdMargin = 0
	tracker.SetTuning(tuning)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	clock.advance(time.Second)                           // well within StickyGraceWindow

	// With the sticky heuristic off (see Assign), Peek must not guess
	// "still Person 1" from elapsed time alone either -- a provisional
	// label should never show sticky continuity the final Assign has
	// been configured to never apply.
	if got := tracker.Peek([]float32{0, 1, 0, 0}); got != "Person 2" {
		t.Errorf("Peek with sticky disabled = %q, want %q (next new speaker)", got, "Person 2")
	}
}
