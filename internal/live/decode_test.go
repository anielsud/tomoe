package live

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// lenEngine records how many samples it was given and reports tokens at
// fixed times in that audio.
type lenEngine struct {
	mockEngine
	got int
}

func (e *lenEngine) TranscribeDirect(samples []float32) (*transcribe.Result, error) {
	e.got = len(samples)
	return &transcribe.Result{Text: "hi there", Timestamps: []float32{0.1, 0.25, 0.6}}, nil
}

func TestDecodePad(t *testing.T) {
	eng := &lenEngine{}
	c := &Coordinator{cfg: Config{Engine: eng, DecodePad: 0.25}}
	r, err := c.decode(make([]float32, 16000))
	if err != nil {
		t.Fatal(err)
	}
	if eng.got != 16000+2*4000 {
		t.Errorf("decoded %d samples, want 0.25 s of silence on each side (%d)", eng.got, 16000+2*4000)
	}
	// Token times come back relative to the line's own audio.
	want := []float32{0, 0, 0.35}
	for i, ts := range r.Timestamps {
		if d := ts - want[i]; d < -1e-6 || d > 1e-6 {
			t.Errorf("timestamp %d = %v, want %v", i, ts, want[i])
		}
	}

	c.cfg.DecodePad = 0
	if _, err := c.decode(make([]float32, 16000)); err != nil || eng.got != 16000 {
		t.Errorf("no pad: decoded %d samples (err %v)", eng.got, err)
	}
}
