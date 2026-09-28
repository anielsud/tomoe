package videohint

import "testing"

func TestSendEventNilChannel(t *testing.T) {
	sendEvent(nil, Event{Stage: StageFrameCaptured}) // must not block or panic
}

func TestSendEventDropsWhenConsumerIsBehind(t *testing.T) {
	events := make(chan Event, 1)
	sendEvent(events, Event{Stage: StageFrameCaptured})
	sendEvent(events, Event{Stage: StageRingMatched}) // buffer full: dropped, not blocked

	if got := <-events; got.Stage != StageFrameCaptured {
		t.Errorf("first event stage = %q, want %q", got.Stage, StageFrameCaptured)
	}
	select {
	case ev := <-events:
		t.Errorf("expected second event to be dropped, got %q", ev.Stage)
	default:
	}
}
