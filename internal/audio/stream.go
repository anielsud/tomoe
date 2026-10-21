package audio

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultWindowSize is the number of float32 samples per window (matches Silero VAD window size).
const DefaultWindowSize = 512

// WindowBuffer is the window channel's length (about 32 s at
// DefaultWindowSize). Audio beyond it is held back in the capturer, in
// order, for as long as the consumer is behind: an overloaded machine
// costs time, never audio. (With 128 windows and drops, a slow decode
// once lost minutes of a meeting, and left the diarizer's clock minutes
// behind the transcript's.)
const WindowBuffer = 1000

// stopDrainTimeout bounds how long Stop's drain waits for the consumer.
var stopDrainTimeout = 2 * time.Minute

// StreamCapturer wraps a Capturer and delivers fixed-size windows of audio
// to a channel for continuous processing (e.g., real-time VAD feeding).
type StreamCapturer struct {
	capturer Capturer
	windowCh chan []float32
	stopCh   chan struct{}
	// dropped counts samples never handed to the consumer (only when it
	// didn't drain them within stopDrainTimeout of Stop); behind, samples
	// held back while it catches up.
	dropped atomic.Int64
	behind  atomic.Int64

	mu       sync.Mutex
	started  bool
	closed   bool
	stopOnce sync.Once

	windowSize int
	// allSamples accumulates all captured audio for later retrieval.
	allMu      sync.Mutex
	allSamples []float32
}

// NewStreamCapturer creates a StreamCapturer wrapping the given Capturer.
// It delivers windows of windowSize samples to the returned channel.
// The channel buffer size is chanBuf (use 0 for unbuffered).
func NewStreamCapturer(capturer Capturer, windowSize, chanBuf int) *StreamCapturer {
	if windowSize <= 0 {
		windowSize = DefaultWindowSize
	}
	if chanBuf < 0 {
		chanBuf = 0
	}
	return &StreamCapturer{
		capturer:   capturer,
		windowCh:   make(chan []float32, chanBuf),
		stopCh:     make(chan struct{}),
		windowSize: windowSize,
	}
}

// Windows returns the channel that receives fixed-size audio windows.
func (s *StreamCapturer) Windows() <-chan []float32 {
	return s.windowCh
}

// Start begins capturing audio and emitting windows to the channel.
func (s *StreamCapturer) Start() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("stream capturer is closed")
	}
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("stream capturer already started")
	}
	s.started = true
	s.mu.Unlock()

	if err := s.capturer.Start(); err != nil {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return fmt.Errorf("starting capturer: %w", err)
	}

	go s.pollLoop()
	return nil
}

// Stop stops capturing and closes the window channel.
func (s *StreamCapturer) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	s.mu.Unlock()

	s.stopOnce.Do(func() { close(s.stopCh) })
	if err := s.capturer.Stop(); err != nil {
		return fmt.Errorf("stopping capturer: %w", err)
	}
	return nil
}

// AllSamples returns all audio samples captured since Start.
func (s *StreamCapturer) AllSamples() []float32 {
	s.allMu.Lock()
	defer s.allMu.Unlock()
	out := make([]float32, len(s.allSamples))
	copy(out, s.allSamples)
	return out
}

// Close stops the stream capturer and releases the underlying capturer.
func (s *StreamCapturer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	wasStarted := s.started
	s.mu.Unlock()

	if wasStarted {
		_ = s.Stop()
	}
	s.capturer.Close()
}

// pollLoop continuously polls the capturer for new samples and emits windows.
func (s *StreamCapturer) pollLoop() {
	buf := make([]float32, 0, s.windowSize*2)
	ticker := makeTickerFunc()
	defer ticker.Stop()
	var pending [][]float32
	take := func(samples []float32) {
		s.allMu.Lock()
		s.allSamples = append(s.allSamples, samples...)
		s.allMu.Unlock()
		buf = append(buf, samples...)
		for len(buf) >= s.windowSize {
			window := make([]float32, s.windowSize)
			copy(window, buf[:s.windowSize])
			buf = buf[s.windowSize:]
			pending = append(pending, window)
		}
		s.behind.Store(int64(len(pending) * s.windowSize))
	}
	// send hands over held-back windows while the consumer has room.
	send := func() {
		for len(pending) > 0 {
			select {
			case s.windowCh <- pending[0]:
				pending[0] = nil
				pending = pending[1:]
				s.behind.Store(int64(len(pending) * s.windowSize))
			default:
				return
			}
		}
	}

	for {
		select {
		case <-s.stopCh:
			// Drain: the remaining audio, then everything held back, waiting
			// for the consumer (it reads until the channel closes). Past a
			// deadline (a stuck consumer must not hang the stop) what's left
			// is counted in Dropped: the session's saved audio still has it.
			if samples := s.capturer.Samples(); len(samples) > 0 {
				s.capturer.Reset()
				take(samples)
			}
			deadline := time.After(stopDrainTimeout)
		drain:
			for len(pending) > 0 {
				select {
				case s.windowCh <- pending[0]:
					pending = pending[1:]
				case <-deadline:
					for _, w := range pending {
						s.dropped.Add(int64(len(w)))
					}
					break drain
				}
			}
			close(s.windowCh)
			return

		case <-ticker.C():
			samples := s.capturer.Samples()
			s.capturer.Reset()
			if len(samples) > 0 {
				take(samples)
			}
			send()
		}
	}
}

// Dropped is how many samples were never handed to the consumer of
// Windows (only when it didn't finish within stopDrainTimeout of Stop).
func (s *StreamCapturer) Dropped() int64 { return s.dropped.Load() }

// Behind is how many samples are held back waiting for the consumer.
func (s *StreamCapturer) Behind() int64 { return s.behind.Load() }
