package live

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
)

// micLabel: the mic is "You" unless an utterance long enough to tell is
// unlike the user's voice; then it's clustered as someone else.
func TestMicLabel(t *testing.T) {
	vec := func(xs ...float32) []float32 {
		out := make([]float32, 8)
		copy(out, xs)
		return out
	}
	var heard int
	c := New(Config{
		MicEmbedder:   &speaker.Embedder{}, // only checked for nil here
		OwnerVoice:    vec(1),
		MicOtherBelow: 0.35,
		Tracker:       speaker.NewTracker(speaker.DefaultThreshold),
		HeardOwner:    func([]float32) { heard++ },
	})
	if l, _ := c.micLabel(vec(0.9, 0.1), 3*time.Second); l != "You" || heard != 1 {
		t.Errorf("own voice: %q, learned %d", l, heard)
	}
	if l, _ := c.micLabel(vec(0.4, 0.6), 3*time.Second); l != "You" || heard != 1 {
		t.Errorf("own voice, not sure enough to learn: %q, learned %d", l, heard)
	}
	if l, _ := c.micLabel(vec(0, 1), time.Second); l != "You" {
		t.Errorf("short unlike utterance: %q", l)
	}
	if l, _ := c.micLabel(vec(0, 1), 3*time.Second); l == "You" || l == "" {
		t.Errorf("someone else: %q", l)
	}
	if l, _ := c.micLabel(nil, 3*time.Second); l != "You" {
		t.Errorf("no fingerprint: %q", l)
	}
	off := New(Config{Tracker: speaker.NewTracker(speaker.DefaultThreshold)})
	if l, _ := off.micLabel(vec(0, 1), 3*time.Second); l != "You" {
		t.Errorf("mic_voices off: %q", l)
	}
}
