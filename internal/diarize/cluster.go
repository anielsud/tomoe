package diarize

import (
	"math"
	"sort"
)

// clusterComplete clusters unit-length embeddings by complete-linkage
// agglomerative clustering on cosine distance (1 - cosine similarity), as
// sherpa-onnx's FastClustering does with fastcluster: clusters keep merging
// while the most distant pair between two clusters is within threshold, or
// until numClusters remain when that's > 0. Returns a cluster index per
// embedding, numbered 0..K-1 in order of first member.
//
// Uses the nearest-neighbor chain algorithm (complete linkage is
// reducible), O(n^2) time and memory: a few thousand embeddings take well
// under a second.
func clusterComplete(embs [][]float32, threshold float64, numClusters int) []int {
	n := len(embs)
	if n == 0 {
		return nil
	}
	if n == 1 {
		return []int{0}
	}
	// Condensed distance matrix: d(i, j) for i < j.
	d := make([]float32, n*(n-1)/2)
	idx := func(i, j int) int {
		if i > j {
			i, j = j, i
		}
		return i*(2*n-i-1)/2 + (j - i - 1)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			var dot float32
			a, b := embs[i], embs[j]
			for k := range a {
				dot += a[k] * b[k]
			}
			d[idx(i, j)] = 1 - dot
		}
	}

	type merge struct {
		a, b   int
		height float32
	}
	active := make([]bool, n)
	for i := range active {
		active[i] = true
	}
	var merges []merge
	chain := make([]int, 0, n)
	remaining := n
	for remaining > 1 {
		if len(chain) == 0 {
			for i := 0; i < n; i++ {
				if active[i] {
					chain = append(chain, i)
					break
				}
			}
		}
		for {
			top := chain[len(chain)-1]
			prev := -1
			if len(chain) > 1 {
				prev = chain[len(chain)-2]
			}
			// Nearest active neighbor of top; ties prefer prev, so the
			// chain always ends in a reciprocal pair.
			best, bestD := -1, float32(math.Inf(1))
			if prev >= 0 {
				best, bestD = prev, d[idx(top, prev)]
			}
			for j := 0; j < n; j++ {
				if j == top || !active[j] {
					continue
				}
				if dj := d[idx(top, j)]; dj < bestD {
					best, bestD = j, dj
				}
			}
			if best == prev {
				chain = chain[:len(chain)-2]
				// Merge prev into top (top keeps the slot), complete
				// linkage: distance to the union is the max.
				for k := 0; k < n; k++ {
					if k == top || k == prev || !active[k] {
						continue
					}
					if dp := d[idx(prev, k)]; dp > d[idx(top, k)] {
						d[idx(top, k)] = dp
					}
				}
				active[prev] = false
				merges = append(merges, merge{top, prev, bestD})
				remaining--
				break
			}
			chain = append(chain, best)
		}
	}

	// Cut the dendrogram. Complete linkage is monotone, so the clusters at
	// a height are those formed by merges at or below it; for a cluster
	// count, apply the n-k lowest merges.
	sort.SliceStable(merges, func(i, j int) bool { return merges[i].height < merges[j].height })
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	limit := len(merges)
	if numClusters > 0 {
		limit = max(0, n-numClusters)
	}
	for i, mg := range merges {
		if i >= limit || (numClusters <= 0 && float64(mg.height) > threshold) {
			break
		}
		parent[find(mg.b)] = find(mg.a)
	}
	return compactLabels(n, find)
}

// compactLabels numbers union-find roots 0..K-1 by first member.
func compactLabels(n int, find func(int) int) []int {
	out := make([]int, n)
	ids := map[int]int{}
	for i := 0; i < n; i++ {
		r := find(i)
		id, ok := ids[r]
		if !ok {
			id = len(ids)
			ids[r] = id
		}
		out[i] = id
	}
	return out
}

// mergeByCentroid repeatedly merges the two clusters whose mean embeddings
// are most similar, while that similarity is at least minSimilarity.
// Deterministic (no map iteration), and each cluster's voice is the mean of
// all its windows' embeddings.
func mergeByCentroid(embs [][]float32, clusters []int, minSimilarity float64) []int {
	k := 0
	for _, c := range clusters {
		k = max(k, c+1)
	}
	dim := len(embs[0])
	sums := make([][]float64, k)
	for i := range sums {
		sums[i] = make([]float64, dim)
	}
	for i, c := range clusters {
		for j, v := range embs[i] {
			sums[c][j] += float64(v)
		}
	}
	alive := make([]bool, k)
	for i := range alive {
		alive[i] = true
	}
	cos := func(a, b []float64) float64 {
		var dot, na, nb float64
		for i := range a {
			dot += a[i] * b[i]
			na += a[i] * a[i]
			nb += b[i] * b[i]
		}
		if na == 0 || nb == 0 {
			return -1
		}
		return dot / math.Sqrt(na*nb)
	}
	into := make([]int, k)
	for i := range into {
		into[i] = i
	}
	for {
		bi, bj, best := -1, -1, minSimilarity
		for i := 0; i < k; i++ {
			if !alive[i] {
				continue
			}
			for j := i + 1; j < k; j++ {
				if alive[j] {
					if s := cos(sums[i], sums[j]); s >= best {
						bi, bj, best = i, j, s
					}
				}
			}
		}
		if bi < 0 {
			break
		}
		for d := range sums[bi] {
			sums[bi][d] += sums[bj][d]
		}
		alive[bj] = false
		into[bj] = bi
	}
	root := func(c int) int {
		for into[c] != c {
			c = into[c]
		}
		return c
	}
	return compactLabels(len(clusters), func(i int) int { return root(clusters[i]) })
}

// AbsorbSmallClusters gives every window of a cluster that speaks for less
// than minSeconds in all (by the turns params reconstructs) to the larger
// cluster whose mean voice it's most like. Such clusters are mostly
// flickers: a moment of one person's voice the fingerprints placed apart
// (see session.AbsorbSmallSpeakers, which does the same by neighbouring
// line instead of by voice). minSeconds <= 0, or no cluster that large,
// leaves clusters as they are.
func (p *Prepared) AbsorbSmallClusters(clusters []int, params Params, minSeconds float64) []int {
	if minSeconds <= 0 || len(clusters) == 0 {
		return clusters
	}
	k := 0
	for _, c := range clusters {
		k = max(k, c+1)
	}
	spoke := make([]float64, k)
	for _, t := range p.turns(clusters, params) {
		spoke[t.Speaker] += t.End - t.Start
	}
	return absorbByVoice(p.Embeddings, clusters, spoke, minSeconds)
}

// absorbByVoice is AbsorbSmallClusters given how long each cluster spoke.
func absorbByVoice(embs [][]float32, clusters []int, spoke []float64, minSeconds float64) []int {
	k := len(spoke)
	dim := len(embs[0])
	sums := make([][]float64, k)
	anyLarge := false
	for c := range sums {
		if spoke[c] >= minSeconds {
			sums[c] = make([]float64, dim)
			anyLarge = true
		}
	}
	if !anyLarge {
		return clusters
	}
	for i, c := range clusters {
		if sums[c] != nil {
			for j, v := range embs[i] {
				sums[c][j] += float64(v)
			}
		}
	}
	for _, s := range sums {
		if s != nil {
			var n float64
			for _, v := range s {
				n += v * v
			}
			n = math.Sqrt(n)
			for j := range s {
				s[j] /= n
			}
		}
	}
	out := append([]int(nil), clusters...)
	for i, c := range clusters {
		if sums[c] != nil {
			continue
		}
		best, bestSim := c, math.Inf(-1)
		for b, s := range sums {
			if s == nil {
				continue
			}
			var dot float64
			for j, v := range embs[i] {
				dot += float64(v) * s[j]
			}
			if dot > bestSim {
				best, bestSim = b, dot
			}
		}
		out[i] = best
	}
	return out
}
