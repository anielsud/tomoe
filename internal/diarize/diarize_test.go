package diarize

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
)

// naiveComplete is textbook O(n^3) complete-linkage clustering on cosine
// distance, merging the closest pair while it's within threshold (or until
// k clusters remain), for checking clusterComplete.
func naiveComplete(embs [][]float32, threshold float64, k int) []int {
	clusters := make([][]int, len(embs))
	for i := range clusters {
		clusters[i] = []int{i}
	}
	dist := func(a, b []int) float64 {
		worst := 0.0
		for _, i := range a {
			for _, j := range b {
				var dot float64
				for d := range embs[i] {
					dot += float64(embs[i][d]) * float64(embs[j][d])
				}
				worst = math.Max(worst, 1-dot)
			}
		}
		return worst
	}
	for len(clusters) > 1 {
		bi, bj, best := -1, -1, math.Inf(1)
		for i := range clusters {
			for j := i + 1; j < len(clusters); j++ {
				if d := dist(clusters[i], clusters[j]); d < best {
					bi, bj, best = i, j, d
				}
			}
		}
		if (k > 0 && len(clusters) <= k) || (k <= 0 && best > threshold) {
			break
		}
		clusters[bi] = append(clusters[bi], clusters[bj]...)
		clusters = append(clusters[:bj], clusters[bj+1:]...)
	}
	out := make([]int, len(embs))
	for c, members := range clusters {
		for _, i := range members {
			out[i] = c
		}
	}
	return out
}

// samePartition reports whether two labelings group items identically.
func samePartition(a, b []int) bool {
	m1, m2 := map[int]int{}, map[int]int{}
	for i := range a {
		if x, ok := m1[a[i]]; ok && x != b[i] {
			return false
		}
		if x, ok := m2[b[i]]; ok && x != a[i] {
			return false
		}
		m1[a[i]], m2[b[i]] = b[i], a[i]
	}
	return true
}

func randomUnit(rng *rand.Rand, n, dim, groups int) [][]float32 {
	centers := make([][]float32, groups)
	for g := range centers {
		centers[g] = make([]float32, dim)
		for d := range centers[g] {
			centers[g][d] = float32(rng.NormFloat64())
		}
	}
	out := make([][]float32, n)
	for i := range out {
		c := centers[rng.Intn(groups)]
		v := make([]float32, dim)
		for d := range v {
			v[d] = c[d] + float32(rng.NormFloat64()*0.6)
		}
		normalize(v)
		out[i] = v
	}
	return out
}

func TestClusterComplete_MatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 60; trial++ {
		embs := randomUnit(rng, 2+rng.Intn(25), 8, 1+rng.Intn(5))
		for _, th := range []float64{0.2, 0.5, 0.9, 1.1} {
			if got, want := clusterComplete(embs, th, 0), naiveComplete(embs, th, 0); !samePartition(got, want) {
				t.Fatalf("trial %d threshold %.1f: got %v want %v", trial, th, got, want)
			}
		}
		k := 1 + rng.Intn(4)
		if got, want := clusterComplete(embs, 0, k), naiveComplete(embs, 0, k); !samePartition(got, want) {
			t.Fatalf("trial %d k=%d: got %v want %v", trial, k, got, want)
		}
	}
}

func TestPowersetMapping(t *testing.T) {
	m, err := powersetMapping(7, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int8{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {1, 1, 0}, {1, 0, 1}, {0, 1, 1}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("powerset = %v", m)
	}
}

func TestChunkStarts(t *testing.T) {
	m := Meta{WindowSize: 100, WindowShift: 10}
	if got := chunkStarts(50, m); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("short audio: %v", got)
	}
	if got := chunkStarts(120, m); !reflect.DeepEqual(got, []int{0, 10, 20}) {
		t.Errorf("exact fit: %v", got)
	}
	if got := chunkStarts(125, m); !reflect.DeepEqual(got, []int{0, 10, 20, 30}) {
		t.Errorf("partial last window: %v", got)
	}
}

func TestMergeByCentroid_DeterministicAndThresholded(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0.95, 0.312}
	c := []float32{0, 1}
	embs := [][]float32{a, a, b, b, c}
	normalize(b)
	clusters := []int{0, 0, 1, 1, 2}
	for i := 0; i < 5; i++ {
		got := mergeByCentroid(embs, clusters, 0.9)
		if !reflect.DeepEqual(got, []int{0, 0, 0, 0, 1}) {
			t.Fatalf("merge = %v", got)
		}
	}
	if got := mergeByCentroid(embs, clusters, 0.99); !reflect.DeepEqual(got, []int{0, 0, 1, 1, 2}) {
		t.Errorf("high threshold should merge nothing: %v", got)
	}
}
