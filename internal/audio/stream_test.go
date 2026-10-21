package audio

import (
	"sync"
	"testing"
	"time"
)

// mockCapturer is a test double for audio.Capturer that delivers pre-loaded samples.
type mockCapturer struct {
	mu       sync.Mutex
	samples  []float32
	started  bool
	stopped  bool
	closed   bool
	startErr error
	stopErr  error
}

func (m *mockCapturer) Start() error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockCapturer) Stop() error {
	m.stopped = true
	return m.stopErr
}

func (m *mockCapturer) Samples() []float32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float32, len(m.samples))
	copy(out, m.samples)
	return out
}

func (m *mockCapturer) Reset() {
	m.mu.Lock()
	m.samples = m.samples[:0]
	m.mu.Unlock()
}

func (m *mockCapturer) Close() {
	m.closed = true
}

// feed adds samples to the mock capturer (simulating audio arriving).
func (m *mockCapturer) feed(samples []float32) {
	m.mu.Lock()
	m.samples = append(m.samples, samples...)
	m.mu.Unlock()
}

// fakeTicker is a test ticker that fires on demand.
type fakeTicker struct {
	ch chan time.Time
}

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               {}

func TestStreamCapturerWindowOutput(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 10)

	// Install fake ticker
	ft := &fakeTicker{ch: make(chan time.Time, 10)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()

	if err := sc.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Feed 8 samples → expect 2 windows of 4
	mock.feed([]float32{1, 2, 3, 4, 5, 6, 7, 8})
	ft.ch <- time.Now()

	// Allow poll loop to process
	time.Sleep(20 * time.Millisecond)

	var windows [][]float32
	// Drain available windows (non-blocking)
loop:
	for {
		select {
		case w, ok := <-sc.Windows():
			if !ok {
				break loop
			}
			windows = append(windows, w)
		default:
			break loop
		}
	}

	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2", len(windows))
	}

	// Verify window contents
	for i, want := range [][]float32{{1, 2, 3, 4}, {5, 6, 7, 8}} {
		for j, v := range windows[i] {
			if v != want[j] {
				t.Errorf("window[%d][%d] = %v, want %v", i, j, v, want[j])
			}
		}
	}

	if err := sc.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestStreamCapturerCleanStop(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 10)

	ft := &fakeTicker{ch: make(chan time.Time, 10)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()

	if err := sc.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if err := sc.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	// Channel should be closed
	_, ok := <-sc.Windows()
	if ok {
		t.Error("Windows channel should be closed after Stop()")
	}

	if !mock.stopped {
		t.Error("underlying capturer should be stopped")
	}
}

func TestStreamCapturerAllSamples(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 10)

	ft := &fakeTicker{ch: make(chan time.Time, 10)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()

	if err := sc.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	mock.feed([]float32{1, 2, 3, 4, 5})
	ft.ch <- time.Now()
	time.Sleep(20 * time.Millisecond)

	all := sc.AllSamples()
	if len(all) != 5 {
		t.Fatalf("AllSamples() len = %d, want 5", len(all))
	}

	if err := sc.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestStreamCapturerClose(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 10)

	ft := &fakeTicker{ch: make(chan time.Time, 10)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()

	if err := sc.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	sc.Close()

	if !mock.closed {
		t.Error("underlying capturer should be closed")
	}

	// Double close should be safe
	sc.Close()
}

func TestStreamCapturerDefaultWindowSize(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 0, 10) // 0 → DefaultWindowSize
	if sc.windowSize != DefaultWindowSize {
		t.Errorf("windowSize = %d, want %d", sc.windowSize, DefaultWindowSize)
	}
}

// A consumer that falls far behind still gets every window, in order:
// audio beyond the channel's buffer is held back, not dropped.
func TestStreamCapturerHoldsBackForSlowConsumer(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 2)
	ft := &fakeTicker{ch: make(chan time.Time, 100)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()
	if err := sc.Start(); err != nil {
		t.Fatal(err)
	}
	// 50 ticks of 8 samples with nobody reading: 100 windows against a
	// channel of 2.
	for i := 0; i < 50; i++ {
		var batch []float32
		for j := 0; j < 8; j++ {
			batch = append(batch, float32(i*8+j))
		}
		mock.feed(batch)
		ft.ch <- time.Now()
	}
	time.Sleep(50 * time.Millisecond)
	if sc.Behind() == 0 {
		t.Error("nothing reported held back")
	}
	done := make(chan []float32)
	go func() {
		var all []float32
		for w := range sc.Windows() {
			all = append(all, w...)
			time.Sleep(time.Millisecond) // a slow consumer
		}
		done <- all
	}()
	for sc.Behind() > 0 {
		ft.ch <- time.Now()
		time.Sleep(5 * time.Millisecond)
	}
	if err := sc.Stop(); err != nil {
		t.Fatal(err)
	}
	all := <-done
	if len(all) != 400 {
		t.Fatalf("got %d samples, want 400", len(all))
	}
	for i, v := range all {
		if v != float32(i) {
			t.Fatalf("sample %d is %v: out of order", i, v)
		}
	}
	if sc.Dropped() != 0 {
		t.Errorf("dropped %d samples", sc.Dropped())
	}
}
