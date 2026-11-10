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

// A few different words aren't enough to be someone else on the mic.
func TestDistinctWords(t *testing.T) {
	for text, want := range map[string]int{"No. No. No. No.": 1, "Ha ha ha ha ha. Yeah.": 2, "I'll send the deck over tonight": 6, "": 0} {
		if got := distinctWords(text); got != want {
			t.Errorf("%q: %d, want %d", text, got, want)
		}
	}
}

// A mic utterance is cut where the voice changes for at least two
// windows, never into a piece shorter than micVoicePiece.
func TestVoiceCuts(t *testing.T) {
	const y, n = false, true // the user's voice; someone else's
	for _, tc := range []struct {
		name  string
		other []bool
		want  []float64
	}{
		{"one voice", []bool{y, y, y, y, y, y, y, y}, nil},
		{"one odd window", []bool{y, y, y, y, n, y, y, y, y}, nil},
		// 12 s: windows start every second, the change at window 5.
		{"handover", []bool{y, y, y, y, y, n, n, n, n, n, n}, []float64{105.5}},
		{"and back", []bool{y, y, y, y, n, n, n, n, y, y, y, y, y}, []float64{104.5, 108.5}},
		// Too near the start to leave a 3 s piece: no cut.
		{"too early", []bool{y, n, n, n, n, n, n, n}, nil},
	} {
		end := 100 + float64(len(tc.other)) + 1
		got := voiceCuts(tc.other, 100, end)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}
