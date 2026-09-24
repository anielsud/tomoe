package speaker

import (
	"testing"
	"time"
)

func TestTrackerAssignSameSpeaker(t *testing.T) {
	tracker := NewTracker(0.8)

	// Two very similar embeddings should be same speaker
	emb1 := []float32{1, 0, 0, 0, 0}
	emb2 := []float32{0.99, 0.01, 0, 0, 0}

	label1 := tracker.Assign(emb1)
	label2 := tracker.Assign(emb2)

	if label1 != "Person 1" {
		t.Errorf("first label = %q, want %q", label1, "Person 1")
	}
	if label2 != "Person 1" {
		t.Errorf("second label = %q, want %q (same speaker)", label2, "Person 1")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d, want 1", tracker.NumSpeakers())
	}
}

func TestTrackerAssignDifferentSpeakers(t *testing.T) {
	tracker := NewTracker(0.8)

	// Orthogonal embeddings → different speakers
	emb1 := []float32{1, 0, 0, 0, 0}
	emb2 := []float32{0, 1, 0, 0, 0}

	label1 := tracker.Assign(emb1)
	label2 := tracker.Assign(emb2)

	if label1 != "Person 1" {
		t.Errorf("first label = %q, want %q", label1, "Person 1")
	}
	if label2 != "Person 2" {
		t.Errorf("second label = %q, want %q (different speaker)", label2, "Person 2")
	}
	if tracker.NumSpeakers() != 2 {
		t.Errorf("NumSpeakers() = %d, want 2", tracker.NumSpeakers())
	}
}

func TestTrackerThreshold(t *testing.T) {
	// With a very high threshold, similar vectors should still be different speakers
	tracker := NewTracker(0.999)

	emb1 := []float32{1, 0, 0}
	emb2 := []float32{0.95, 0.3, 0}

	tracker.Assign(emb1)
	label2 := tracker.Assign(emb2)

	if label2 != "Person 2" {
		t.Errorf("with high threshold, label = %q, want %q", label2, "Person 2")
	}
}

func TestTrackerReset(t *testing.T) {
	tracker := NewTracker(0.8)

	tracker.Assign([]float32{1, 0, 0})
	tracker.Assign([]float32{0, 1, 0})

	if tracker.NumSpeakers() != 2 {
		t.Fatalf("NumSpeakers() before reset = %d, want 2", tracker.NumSpeakers())
	}

	tracker.Reset()

	if tracker.NumSpeakers() != 0 {
		t.Errorf("NumSpeakers() after reset = %d, want 0", tracker.NumSpeakers())
	}

	// After reset, next embedding is Person 1 again
	label := tracker.Assign([]float32{1, 0, 0})
	if label != "Person 1" {
		t.Errorf("after reset, label = %q, want %q", label, "Person 1")
	}
}

func TestTrackerEmptyEmbedding(t *testing.T) {
	tracker := NewTracker(0.8)
	label := tracker.Assign(nil)
	if label != "Unknown" {
		t.Errorf("empty embedding label = %q, want %q", label, "Unknown")
	}
}

func TestTrackerDefaultThreshold(t *testing.T) {
	// Invalid thresholds should use default
	tracker := NewTracker(0)
	if tracker.threshold != DefaultThreshold {
		t.Errorf("threshold = %v, want %v", tracker.threshold, DefaultThreshold)
	}

	tracker = NewTracker(-1)
	if tracker.threshold != DefaultThreshold {
		t.Errorf("threshold = %v, want %v", tracker.threshold, DefaultThreshold)
	}
}

func TestTrackerMultipleSpeakers(t *testing.T) {
	tracker := NewTracker(0.7)

	// Simulate 3 distinct speakers
	speakers := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
	}

	for i, emb := range speakers {
		label := tracker.Assign(emb)
		want := "Person " + string(rune('1'+i))
		if label != want {
			t.Errorf("speaker %d label = %q, want %q", i, label, want)
		}
	}

	// Re-assign same embeddings — should match existing speakers
	for i, emb := range speakers {
		label := tracker.Assign(emb)
		want := "Person " + string(rune('1'+i))
		if label != want {
			t.Errorf("re-assign speaker %d label = %q, want %q", i, label, want)
		}
	}

	if tracker.NumSpeakers() != 3 {
		t.Errorf("NumSpeakers() = %d, want 3", tracker.NumSpeakers())
	}
}

func TestTrackerSetHintForRecent(t *testing.T) {
	tracker := NewTracker(0.8)

	tracker.Assign([]float32{1, 0, 0}) // Person 1
	tracker.Assign([]float32{0, 1, 0}) // Person 2, most recently assigned

	if ok := tracker.SetHintForRecent("Nazanin", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	// The hint should attach to Person 2 (most recently assigned), not Person 1.
	label1 := tracker.Assign([]float32{0.99, 0.01, 0})
	if label1 != "Person 1" {
		t.Errorf("Person 1 label = %q, want unchanged %q", label1, "Person 1")
	}
	label2 := tracker.Assign([]float32{0, 0.99, 0.01})
	if label2 != "Person 2 (Nazanin)" {
		t.Errorf("Person 2 label = %q, want %q", label2, "Person 2 (Nazanin)")
	}
}

func TestTrackerSetHintForRecent_TooOld(t *testing.T) {
	tracker := NewTracker(0.8)
	tracker.Assign([]float32{1, 0, 0})

	if ok := tracker.SetHintForRecent("Someone", -time.Second); ok {
		t.Error("SetHintForRecent() with a negative maxAge = true, want false")
	}
}

func TestTrackerSetHintForRecent_NoSpeakersYet(t *testing.T) {
	tracker := NewTracker(0.8)
	if ok := tracker.SetHintForRecent("Someone", time.Minute); ok {
		t.Error("SetHintForRecent() before any Assign = true, want false")
	}
}

func TestTrackerResetClearsHints(t *testing.T) {
	tracker := NewTracker(0.8)
	tracker.Assign([]float32{1, 0, 0})
	tracker.SetHintForRecent("Nazanin", time.Minute)
	tracker.Reset()

	label := tracker.Assign([]float32{1, 0, 0})
	if label != "Person 1" {
		t.Errorf("after reset, label = %q, want plain %q (hint should be cleared)", label, "Person 1")
	}
}
