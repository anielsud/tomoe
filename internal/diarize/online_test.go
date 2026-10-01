package diarize

import "testing"

func TestStableLabelsKeepNumbersAcrossReclusters(t *testing.T) {
	s := NewStableLabels()
	pairs := []ChunkSpeaker{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	first := s.Assign(pairs[:2], []int{0, 1})
	if first[0] == first[1] {
		t.Fatalf("two clusters got one ID: %v", first)
	}
	// The recluster numbers the same speakers the other way round and
	// adds a window: IDs follow the speakers, not the cluster numbers.
	second := s.Assign(pairs, []int{1, 0, 1, 0})
	if second[1] != first[0] || second[0] != first[1] {
		t.Errorf("recluster IDs %v don't follow first IDs %v", second, first)
	}
	// A new speaker gets a new ID.
	third := s.Assign(append(pairs, ChunkSpeaker{2, 0}), []int{1, 0, 1, 0, 2})
	if third[2] == first[0] || third[2] == first[1] {
		t.Errorf("new speaker reused an ID: %v", third)
	}
}

func TestTruncateAndEveryNth(t *testing.T) {
	p := &Prepared{
		Meta:       Meta{WindowSize: 10, WindowShift: 2},
		Labels:     make([][][]int8, 5),
		Pairs:      []ChunkSpeaker{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}},
		Embeddings: make([][]float32, 5),
	}
	if got := p.Truncate(14); len(got.Labels) != 3 || len(got.Embeddings) != 3 || got.NumSamples != 14 {
		t.Errorf("Truncate(14): %d windows, %d embeddings, %d samples; want 3, 3, 14", len(got.Labels), len(got.Embeddings), got.NumSamples)
	}
	if got := p.EveryNth(2); len(got.Labels) != 5 || len(got.Embeddings) != 3 {
		t.Errorf("EveryNth(2): %d windows, %d embeddings; want 5, 3", len(got.Labels), len(got.Embeddings))
	}
}
