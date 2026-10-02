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

func TestReclusterSpacingAndThin(t *testing.T) {
	for n, want := range map[int]int{100: 1, 3000: 1, 6000: 4, 9000: 9} {
		if got := reclusterSpacing(n); got != want {
			t.Errorf("reclusterSpacing(%d) = %d, want %d", n, got, want)
		}
	}
	pairs := make([]ChunkSpeaker, 10)
	embs := make([][]float32, 10)
	for i := range pairs {
		pairs[i] = ChunkSpeaker{Chunk: i}
	}
	p, e := thin(pairs, embs, 4)
	if len(p) != 4 || len(e) != 4 || p[0].Chunk != 0 || p[3].Chunk != 7 {
		t.Errorf("thin to 4: %v", p)
	}
	if p, _ := thin(pairs, embs, 20); len(p) != 10 {
		t.Errorf("thin below the limit changed the count: %d", len(p))
	}
}

// Frames covered only by windows without a fingerprint have no votes;
// they must stay unlabeled, not fall to cluster 0.
func TestTurnsLeaveUnvotedFramesUnlabeled(t *testing.T) {
	always := [][]int8{{1}, {1}, {1}, {1}}
	p := &Prepared{
		Meta:       Meta{SampleRate: 1, WindowSize: 4, WindowShift: 1, ReceptiveFieldSize: 1, ReceptiveFieldShift: 1},
		NumSamples: 6,
		Labels:     [][][]int8{always, always, always}, // frames 0-3, 1-4, 2-5
		Pairs:      []ChunkSpeaker{{Chunk: 0, Speaker: 0}},
		Embeddings: make([][]float32, 1),
	}
	// One fingerprint (window 0, cluster 1); cluster 0 exists but owns nothing.
	segs := p.ReconstructClusters([]int{1}, Params{})
	for _, s := range segs {
		if s.Speaker == 0 {
			t.Errorf("unvoted frames went to cluster 0: %+v", s)
		}
		if s.Speaker == 1 && s.End > 4.5 {
			t.Errorf("cluster 1's turn %+v runs past the frames its window covers (frames 0-3, ending at 4.5)", s)
		}
	}
	if len(segs) == 0 {
		t.Error("the fingerprinted window's frames got no turn")
	}
}
