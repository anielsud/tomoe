package live

import (
	"testing"
	"time"
)

// Pass 2's minute sums its decode time against the speech covered and
// keeps the longest lag from an utterance's end to its final text.
func TestNotePass2(t *testing.T) {
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	now := start.Add(100 * time.Second)
	c := &Coordinator{now: func() time.Time { return now }, startTime: start}
	second := make([]float32, int(vadSampleRate))
	c.notePass2(refinementJob{endTime: 99, samples: second}, 200*time.Millisecond)
	c.notePass2(refinementJob{endTime: 40, samples: append(second, second...)}, 300*time.Millisecond)
	st := c.pass2
	if st.n != 2 || st.decode != 500*time.Millisecond || st.speech != 3*time.Second {
		t.Fatalf("stats %+v", st)
	}
	if st.maxLag != 60 {
		t.Errorf("longest lag %.1f, want 60 (an utterance ending at 40 s finalized at 100 s)", st.maxLag)
	}
}
