package audio

import "testing"

type fakeCapturer struct {
	buf             []float32
	started, closed bool
}

func (f *fakeCapturer) Start() error       { f.started = true; return nil }
func (f *fakeCapturer) Stop() error        { f.started = false; return nil }
func (f *fakeCapturer) Samples() []float32 { return append([]float32(nil), f.buf...) }
func (f *fakeCapturer) Reset()             { f.buf = nil }
func (f *fakeCapturer) Close()             { f.closed = true }

func TestSwitchCapturerKeepsBufferedAudio(t *testing.T) {
	a, b := &fakeCapturer{}, &fakeCapturer{}
	s := NewSwitchCapturer(a)
	if err := s.Start(); err != nil || !a.started {
		t.Fatal("first source not started")
	}
	a.buf = []float32{1, 2}
	if err := s.Switch(b); err != nil {
		t.Fatal(err)
	}
	if !b.started || a.started || !a.closed {
		t.Errorf("after switch: new started=%v, old started=%v closed=%v", b.started, a.started, a.closed)
	}
	b.buf = []float32{3}
	if got := s.Samples(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("samples across the switch = %v, want [1 2 3]", got)
	}
	s.Reset()
	if got := s.Samples(); len(got) != 0 {
		t.Errorf("after reset: %v", got)
	}
}
