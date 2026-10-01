package diarize

import "sort"

// Truncate returns the part of p an online run would have by sample n:
// the windows that end by then, and their embeddings.
func (p *Prepared) Truncate(n int) *Prepared {
	m := p.Meta
	k := 0
	for k < len(p.Labels) && k*m.WindowShift+m.WindowSize <= n {
		k++
	}
	out := &Prepared{Meta: m, Labels: p.Labels[:k]}
	if k > 0 {
		out.NumSamples = (k-1)*m.WindowShift + m.WindowSize
	}
	for i, pair := range p.Pairs {
		if pair.Chunk < k {
			out.Pairs = append(out.Pairs, pair)
			out.Embeddings = append(out.Embeddings, p.Embeddings[i])
		}
	}
	return out
}

// EveryNth returns p with embeddings kept only for every nth window: the
// others still count how many people are talking, but don't vote on who.
// Embedding is most of diarization's cost, so this divides it by about n.
func (p *Prepared) EveryNth(n int) *Prepared {
	if n <= 1 {
		return p
	}
	out := &Prepared{Meta: p.Meta, NumSamples: p.NumSamples, Labels: p.Labels}
	for i, pair := range p.Pairs {
		if pair.Chunk%n == 0 {
			out.Pairs = append(out.Pairs, pair)
			out.Embeddings = append(out.Embeddings, p.Embeddings[i])
		}
	}
	return out
}

// StableLabels keeps speaker numbers steady across reclusterings: each
// clustering's clusters are matched to the previous one's by the windows
// they share, so a speaker keeps their number as the meeting goes on.
type StableLabels struct {
	byPair map[ChunkSpeaker]int // stable ID each window-speaker last had
	next   int
}

// NewStableLabels returns an empty StableLabels.
func NewStableLabels() *StableLabels {
	return &StableLabels{byPair: map[ChunkSpeaker]int{}}
}

// Assign maps clusters (one per pairs entry, from Cluster/MergeClusters)
// to stable IDs, returning the stable ID of each cluster index. Matching is
// greedy by the number of shared window-speakers, one-to-one; a cluster
// sharing none gets a new ID.
func (s *StableLabels) Assign(pairs []ChunkSpeaker, clusters []int) map[int]int {
	overlap := map[[2]int]int{} // {cluster, stable} -> shared pairs
	numClusters := 0
	for i, c := range clusters {
		numClusters = max(numClusters, c+1)
		if id, ok := s.byPair[pairs[i]]; ok {
			overlap[[2]int{c, id}]++
		}
	}
	type cand struct{ cluster, stable, n int }
	var cands []cand
	for k, n := range overlap {
		cands = append(cands, cand{k[0], k[1], n})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].n != cands[j].n {
			return cands[i].n > cands[j].n
		}
		if cands[i].cluster != cands[j].cluster {
			return cands[i].cluster < cands[j].cluster
		}
		return cands[i].stable < cands[j].stable
	})
	out := map[int]int{}
	used := map[int]bool{}
	for _, c := range cands {
		if _, done := out[c.cluster]; done || used[c.stable] {
			continue
		}
		out[c.cluster], used[c.stable] = c.stable, true
	}
	for c := 0; c < numClusters; c++ {
		if _, ok := out[c]; !ok {
			out[c] = s.next
			s.next++
		}
	}
	for _, id := range out {
		s.next = max(s.next, id+1)
	}
	for i, c := range clusters {
		s.byPair[pairs[i]] = out[c]
	}
	return out
}
