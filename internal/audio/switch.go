package audio

import "sync"

// SwitchCapturer is a Capturer whose source can be swapped while it runs,
// for moving a meeting's system-audio capture from the whole system to the
// meeting app once that app starts making sound. Audio the old source had
// buffered is kept.
type SwitchCapturer struct {
	mu      sync.Mutex
	cur     Capturer
	pending []float32 // the previous source's last samples
	started bool
}

// NewSwitchCapturer starts out capturing c.
func NewSwitchCapturer(c Capturer) *SwitchCapturer { return &SwitchCapturer{cur: c} }

// Switch moves capture to next, starting it first if capture is running,
// and stops and closes the previous source. If next fails to start, the
// previous source keeps capturing and the error is returned.
func (s *SwitchCapturer) Switch(next Capturer) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		if err := next.Start(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	old := s.cur
	s.pending = append(s.pending, old.Samples()...)
	s.cur = next
	s.mu.Unlock()
	_ = old.Stop()
	old.Close()
	return nil
}

func (s *SwitchCapturer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cur.Start(); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *SwitchCapturer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = false
	return s.cur.Stop()
}

func (s *SwitchCapturer) Samples() []float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return s.cur.Samples()
	}
	return append(append([]float32(nil), s.pending...), s.cur.Samples()...)
}

func (s *SwitchCapturer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
	s.cur.Reset()
}

func (s *SwitchCapturer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur.Close()
}
