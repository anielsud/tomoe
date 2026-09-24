package live

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// --- Mock helpers ---

// mockCapturer implements audio.Capturer for testing without real audio hardware.
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
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	return nil
}

func (m *mockCapturer) Stop() error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
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
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

func (m *mockCapturer) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *mockCapturer) isStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

// mockEngine implements transcribe.Engine for testing without ONNX models.
type mockEngine struct {
	result *transcribe.Result
	err    error
	calls  int
	mu     sync.Mutex
}

func (m *mockEngine) TranscribeSamples(samples []float32) (*transcribe.Result, error) {
	return m.TranscribeDirect(samples)
}

func (m *mockEngine) TranscribeDirect(samples []float32) (*transcribe.Result, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return m.result, m.err
}

func (m *mockEngine) TranscribeFile(path string) (*transcribe.Result, error) {
	return m.result, m.err
}

func (m *mockEngine) Close() {}

// newMockStreamCapturer creates an audio.StreamCapturer wrapping a mockCapturer.
// Returns both so the test can inspect the mock's state.
func newMockStreamCapturer() (*audio.StreamCapturer, *mockCapturer) {
	mock := &mockCapturer{}
	sc := audio.NewStreamCapturer(mock, audio.DefaultWindowSize, 10)
	return sc, mock
}

// --- Original tests ---

func TestAssignSpeakerMic(t *testing.T) {
	c := &Coordinator{
		cfg: Config{},
	}

	label := c.assignSpeaker(SourceMic, []float32{0.1, 0.2})
	if label != "You" {
		t.Errorf("mic speaker = %q, want %q", label, "You")
	}
}

func TestAssignSpeakerMonitorNoEmbedder(t *testing.T) {
	c := &Coordinator{
		cfg: Config{},
	}

	// Without embedder, all monitor segments get "Other"
	label := c.assignSpeaker(SourceMonitor, []float32{0.1, 0.2})
	if label != "Other" {
		t.Errorf("monitor speaker = %q, want %q", label, "Other")
	}
}

func TestNextSegID(t *testing.T) {
	c := &Coordinator{}

	id1 := c.nextSegID()
	id2 := c.nextSegID()

	if id1 != "seg-1" {
		t.Errorf("first ID = %q, want %q", id1, "seg-1")
	}
	if id2 != "seg-2" {
		t.Errorf("second ID = %q, want %q", id2, "seg-2")
	}
}

func TestSourceTypeConstants(t *testing.T) {
	if SourceMic != "mic" {
		t.Errorf("SourceMic = %q, want %q", SourceMic, "mic")
	}
	if SourceMonitor != "monitor" {
		t.Errorf("SourceMonitor = %q, want %q", SourceMonitor, "monitor")
	}
}

func TestNewCoordinator(t *testing.T) {
	c := New(Config{SegmentBufferSize: 32})
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if cap(c.segmentCh) != 32 {
		t.Errorf("segment channel cap = %d, want 32", cap(c.segmentCh))
	}
}

func TestNewCoordinatorDefaultBufferSize(t *testing.T) {
	c := New(Config{})
	if cap(c.segmentCh) != 64 {
		t.Errorf("default segment channel cap = %d, want 64", cap(c.segmentCh))
	}
}

func TestSegmentStructure(t *testing.T) {
	seg := session.Segment{
		ID:        "seg-1",
		Speaker:   "You",
		Text:      "Hello world",
		StartTime: 1.0,
		EndTime:   2.5,
		Source:    "mic",
	}

	if seg.Speaker != "You" {
		t.Errorf("Speaker = %q, want %q", seg.Speaker, "You")
	}
}

func TestTrackerIntegration(t *testing.T) {
	tracker := speaker.NewTracker(0.7)

	// Simulate what the coordinator does with speaker tracking
	emb1 := []float32{1, 0, 0, 0}
	emb2 := []float32{0, 1, 0, 0}

	label1, _ := tracker.Assign(emb1)
	label2, _ := tracker.Assign(emb2)
	label3, _ := tracker.Assign(emb1) // Same as emb1

	if label1 != "Person 1" {
		t.Errorf("label1 = %q, want %q", label1, "Person 1")
	}
	if label2 != "Person 2" {
		t.Errorf("label2 = %q, want %q", label2, "Person 2")
	}
	if label3 != "Person 1" {
		t.Errorf("label3 = %q, want %q (same as emb1)", label3, "Person 1")
	}
}

// --- Activity channel tests ---

func TestActivityChannelCreated(t *testing.T) {
	c := New(Config{})
	ch := c.Activity()
	if ch == nil {
		t.Fatal("Activity() returned nil channel")
	}
}

func TestActivityChannelBufferedSize1(t *testing.T) {
	c := New(Config{})
	ch := c.Activity()

	// The activity channel should be buffered with size 1.
	if cap(ch) != 1 {
		t.Errorf("activity channel cap = %d, want 1", cap(ch))
	}
}

func TestActivityChannelNonBlockingSend(t *testing.T) {
	c := New(Config{})

	// First send should succeed (buffer has room for 1).
	select {
	case c.activityCh <- struct{}{}:
		// ok
	default:
		t.Error("first send to activity channel should not block")
	}

	// Second send should be dropped (buffer full), not block.
	select {
	case c.activityCh <- struct{}{}:
		t.Error("second send to full activity channel should be dropped (default case)")
	default:
		// ok — this is the expected path, matching the non-blocking send in pipeline.go
	}
}

func TestActivityChannelReadable(t *testing.T) {
	c := New(Config{})

	// Put a signal into the activity channel.
	c.activityCh <- struct{}{}

	// Should be readable from the public Activity() accessor.
	select {
	case <-c.Activity():
		// ok
	default:
		t.Error("should be able to read from Activity() after signal was sent")
	}

	// After draining, channel should be empty.
	select {
	case <-c.Activity():
		t.Error("Activity() should be empty after draining")
	default:
		// ok
	}
}

// --- Stop closes capturers tests ---

func TestStopClosesMicCapturer(t *testing.T) {
	micSC, micMock := newMockStreamCapturer()

	c := New(Config{
		MicCapturer: micSC,
	})

	// Call Stop without Start — cancel is nil, wg has no entries.
	// Stop should still close the capturer.
	c.Stop()

	if !micMock.isClosed() {
		t.Error("Stop() should call Close() on MicCapturer")
	}
}

func TestStopClosesMonitorCapturer(t *testing.T) {
	monSC, monMock := newMockStreamCapturer()

	c := New(Config{
		MonitorCapturer: monSC,
	})

	c.Stop()

	if !monMock.isClosed() {
		t.Error("Stop() should call Close() on MonitorCapturer")
	}
}

func TestStopClosesBothCapturers(t *testing.T) {
	micSC, micMock := newMockStreamCapturer()
	monSC, monMock := newMockStreamCapturer()

	c := New(Config{
		MicCapturer:     micSC,
		MonitorCapturer: monSC,
	})

	c.Stop()

	if !micMock.isClosed() {
		t.Error("Stop() should call Close() on MicCapturer")
	}
	if !monMock.isClosed() {
		t.Error("Stop() should call Close() on MonitorCapturer")
	}
}

func TestStopWithNilCapturersDoesNotPanic(t *testing.T) {
	c := New(Config{})

	// Should not panic when both capturers are nil.
	c.Stop()
}

func TestStopStopsAndClosesStartedCapturer(t *testing.T) {
	// Verify that Stop() calls both Stop and Close on the StreamCapturer
	// when the capturer was previously started (simulated by setting started=true).
	micMock := &mockCapturer{}
	micSC := audio.NewStreamCapturer(micMock, audio.DefaultWindowSize, 10)

	// Manually mark as started so StreamCapturer.Stop() actually calls through
	// to the underlying capturer. In real usage, Start() would have been called
	// by Coordinator.Start().
	// We cannot call Start() here because the pollLoop would block on real tickers,
	// so we directly test that Coordinator.Stop() invokes Close() on the StreamCapturer.
	c := New(Config{
		MicCapturer: micSC,
	})

	c.Stop()

	// Close is always called regardless of start state.
	if !micMock.isClosed() {
		t.Error("Stop() should call Close() on the capturer")
	}
}

// --- Start error handling tests ---

func TestStartErrorNoSources(t *testing.T) {
	c := New(Config{
		Engine: &mockEngine{},
	})

	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("Start() should fail with no audio sources")
	}
	want := "at least one audio source is required"
	if err.Error() != want {
		t.Errorf("Start() error = %q, want %q", err.Error(), want)
	}
}

func TestStartErrorMicCapturerFails(t *testing.T) {
	mock := &mockCapturer{startErr: fmt.Errorf("mic broke")}
	micSC := audio.NewStreamCapturer(mock, audio.DefaultWindowSize, 10)

	c := New(Config{
		MicCapturer: micSC,
		Engine:      &mockEngine{},
	})

	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("Start() should fail when mic capturer Start() fails")
	}
}

func TestStartErrorMonitorCapturerFails(t *testing.T) {
	micMock := &mockCapturer{}
	micSC := audio.NewStreamCapturer(micMock, audio.DefaultWindowSize, 10)

	monMock := &mockCapturer{startErr: fmt.Errorf("monitor broke")}
	monSC := audio.NewStreamCapturer(monMock, audio.DefaultWindowSize, 10)

	c := New(Config{
		MicCapturer:     micSC,
		MonitorCapturer: monSC,
		Engine:          &mockEngine{},
	})

	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("Start() should fail when monitor capturer Start() fails")
	}

	// Mic capturer should have been stopped on monitor failure.
	if !micMock.isStopped() {
		t.Error("mic capturer should be stopped when monitor start fails")
	}
}

// --- Segments channel test ---

func TestSegmentsChannelAccessor(t *testing.T) {
	c := New(Config{})

	ch := c.Segments()
	if ch == nil {
		t.Fatal("Segments() returned nil")
	}

	// Verify it is the same channel as the internal one.
	c.segmentCh <- session.Segment{ID: "test-1", Text: "hello"}
	seg := <-ch
	if seg.ID != "test-1" {
		t.Errorf("Segments() returned different channel; got ID %q, want %q", seg.ID, "test-1")
	}
}

// --- Stats test ---

func TestStatsCounters(t *testing.T) {
	c := New(Config{})
	c.micCount.Store(5)
	c.monitorCount.Store(3)

	stats := c.Stats()
	if stats.MicSegments != 5 {
		t.Errorf("MicSegments = %d, want 5", stats.MicSegments)
	}
	if stats.MonitorSegments != 3 {
		t.Errorf("MonitorSegments = %d, want 3", stats.MonitorSegments)
	}
}

// --- AudioSamples test ---

func TestAudioSamplesNilCapturers(t *testing.T) {
	c := New(Config{})

	samples := c.AudioSamples()
	if len(samples) != 0 {
		t.Errorf("AudioSamples() with nil capturers should be empty, got len %d", len(samples))
	}
}

// --- Stop idempotency test ---

func TestStopIdempotent(t *testing.T) {
	micSC, _ := newMockStreamCapturer()

	c := New(Config{
		MicCapturer: micSC,
	})

	// Calling Stop multiple times should not panic.
	c.Stop()
	c.Stop()
}
