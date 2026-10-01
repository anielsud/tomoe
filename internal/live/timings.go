package live

import (
	"sync"
	"time"
)

// Timings collects where a Coordinator spends its time, per pipeline stage,
// for measuring latency and processing load (see `tomoe eval`). Optional:
// set Config.Timings to collect; nil costs nothing. Safe for concurrent use.
type Timings struct {
	mu sync.Mutex
	// Stage totals.
	VAD       Stage // voice activity detection, per audio window
	Streaming Stage // pass-1 streaming decode, per audio window
	Decode    Stage // Parakeet decode, per utterance
	Embed     Stage // speaker embedding, per utterance
	Assign    Stage // speaker clustering, per utterance
	// Utterances is the processing time of each completed utterance in
	// seconds, from the end of its speech being detected to its final
	// text and speaker being ready (decode, embedding and clustering;
	// not the silence the detector waits for first).
	Utterances []float64
}

// Stage is one stage's call count and total time.
type Stage struct {
	Calls   int64
	Seconds float64
}

func (t *Timings) add(s *Stage, began time.Time) {
	if t == nil {
		return
	}
	d := time.Since(began).Seconds()
	t.mu.Lock()
	s.Calls++
	s.Seconds += d
	t.mu.Unlock()
}

func (t *Timings) utterance(began time.Time) {
	if t == nil {
		return
	}
	d := time.Since(began).Seconds()
	t.mu.Lock()
	t.Utterances = append(t.Utterances, d)
	t.mu.Unlock()
}

// timingsOrZero returns the coordinator's Timings for taking a stage's
// address; when timing is off it returns a throwaway (add ignores a nil
// receiver, so nothing is recorded).
func (c *Coordinator) timingsOrZero() *Timings {
	if c.cfg.Timings != nil {
		return c.cfg.Timings
	}
	return &discardTimings
}

var discardTimings Timings
