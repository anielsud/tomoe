package diarize

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
)

// ChunkSpeaker identifies one local speaker in one analysis window.
type ChunkSpeaker struct{ Chunk, Speaker int }

// Prepared holds the expensive, settings-independent results of
// diarization for one recording: per-window local speaker activity from the
// segmentation model, and one speaker embedding per (window, local speaker)
// with enough non-overlapping speech. Every clustering and overlap setting
// works from these, so they are computed once (Prepare) and can be cached
// (all fields are exported for encoding/gob).
type Prepared struct {
	Meta       Meta
	NumSamples int
	Labels     [][][]int8 // [chunk][frame][local speaker]
	Pairs      []ChunkSpeaker
	Embeddings [][]float32 // one per Pairs entry, L2-normalized
}

// Prepare runs segmentation and embedding extraction over samples (16kHz
// mono). workers goroutines share the work, each with threads CPU threads.
func Prepare(samples []float32, segmentationModel, embeddingModel string, workers, threads int) (*Prepared, error) {
	return prepare(samples, segmentationModel, embeddingModel, workers, threads, nil)
}

// progressFunc reports work done so far in a phase ("segmenting",
// "fingerprints") out of its total. Called from worker goroutines.
type progressFunc func(phase string, done, total int)

func prepare(samples []float32, segmentationModel, embeddingModel string, workers, threads int, progress progressFunc) (*Prepared, error) {
	seg, err := newSegmenter(segmentationModel, threads)
	if err != nil {
		return nil, err
	}
	defer seg.close()
	labels, err := seg.segment(samples, workers, progress)
	if err != nil {
		return nil, err
	}
	p := &Prepared{Meta: seg.meta, NumSamples: len(samples), Labels: labels}
	pairs, ranges := speakerSampleRanges(labels, seg.meta)
	embs, valid, err := embedRanges(samples, ranges, embeddingModel, workers, progress)
	if err != nil {
		return nil, err
	}
	for i, ok := range valid {
		if ok {
			p.Pairs = append(p.Pairs, pairs[i])
			p.Embeddings = append(p.Embeddings, embs[i])
		}
	}
	return p, nil
}

// speakerSampleRanges returns, for each (window, local speaker) active for
// at least 10 frames once overlapping frames are excluded, the sample
// ranges where that speaker talks alone (sherpa-onnx's
// GetChunkSpeakerSampleIndexes with ExcludeOverlap).
func speakerSampleRanges(labels [][][]int8, m Meta) ([]ChunkSpeaker, [][][2]int) {
	var pairs []ChunkSpeaker
	var ranges [][][2]int
	for c, chunk := range labels {
		frames := len(chunk)
		offset := c * m.WindowShift
		toSample := func(f int) int { return int(float32(f)/float32(frames)*float32(m.WindowSize)) + offset }
		for spk := 0; spk < m.NumSpeakers; spk++ {
			alone := func(f int) bool {
				if chunk[f][spk] == 0 {
					return false
				}
				n := 0
				for _, v := range chunk[f] {
					n += int(v)
				}
				return n < 2
			}
			count := 0
			for f := 0; f < frames; f++ {
				if alone(f) {
					count++
				}
			}
			if count < 10 {
				continue
			}
			var rs [][2]int
			start := -1
			for f := 0; f < frames; f++ {
				if alone(f) {
					if start < 0 {
						start = f
					}
				} else if start >= 0 {
					rs = append(rs, [2]int{toSample(start), toSample(f)})
					start = -1
				}
			}
			if start >= 0 {
				rs = append(rs, [2]int{toSample(start), toSample(frames - 1)})
			}
			pairs = append(pairs, ChunkSpeaker{c, spk})
			ranges = append(ranges, rs)
		}
	}
	return pairs, ranges
}

// embedRanges computes one L2-normalized embedding per entry of ranges
// (the concatenated audio of its sample ranges), spread over workers
// goroutines with an embedder each. valid[i] is false where the model
// returned NaN, which sherpa-onnx also drops.
func embedRanges(samples []float32, ranges [][][2]int, modelPath string, workers int, progress progressFunc) ([][]float32, []bool, error) {
	var done atomic.Int64
	embs := make([][]float32, len(ranges))
	valid := make([]bool, len(ranges))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for w := 0; w < max(1, workers); w++ {
		e, err := speaker.NewEmbedder(modelPath)
		if err != nil {
			close(jobs)
			wg.Wait()
			return nil, nil, fmt.Errorf("loading speaker embedding model: %w", err)
		}
		wg.Add(1)
		go func(e *speaker.Embedder) {
			defer wg.Done()
			defer e.Close()
			var buf []float32
			for i := range jobs {
				buf = buf[:0]
				for _, r := range ranges[i] {
					buf = append(buf, samples[r[0]:min(r[1], len(samples))]...)
				}
				v, err := e.Extract(buf)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				if normalize(v) {
					embs[i], valid[i] = v, true
				}
				if progress != nil {
					progress("fingerprints", int(done.Add(1)), len(ranges))
				}
			}
		}(e)
	}
	for i := range ranges {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return embs, valid, firstErr
}

// normalize scales v to unit length in place, reporting false for an
// empty, zero or NaN vector.
func normalize(v []float32) bool {
	var sum float64
	for _, x := range v {
		if math.IsNaN(float64(x)) {
			return false
		}
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return false
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return true
}

// Params are the cheap diarization settings, applied to Prepared results.
type Params struct {
	// Threshold is the complete-linkage cosine-distance cut: higher merges
	// more. NumClusters > 0 asks for exactly that many clusters instead.
	Threshold   float64
	NumClusters int
	// MergeSimilarity, if > 0, merges clusters whose mean embeddings have
	// at least this cosine similarity (repeatedly, most similar pair
	// first): a deterministic replacement for session.MergeSimilarSpeakers
	// that uses every window's embedding instead of a cluster's first 30s.
	MergeSimilarity float64
	// SpeakerCountRounding is where the averaged number of speakers per
	// frame rounds up: 0.5 is ordinary rounding (sherpa-onnx). Lower keeps
	// more two-speaker frames, so short overlaps aren't averaged away.
	SpeakerCountRounding float64
	// MinDurationOn drops turns shorter than this; MinDurationOff joins
	// one speaker's turns separated by a shorter gap (seconds).
	MinDurationOn  float64
	MinDurationOff float64
}

// DefaultParams reproduces Tomoe's post-meeting diarization today (sherpa-onnx
// with threshold 1.1), apart from the merge step: see MergeSimilarity.
func DefaultParams() Params {
	return Params{Threshold: 1.1, SpeakerCountRounding: 0.5, MinDurationOn: 0.3, MinDurationOff: 0.5}
}

// Diarize clusters the prepared embeddings and reconstructs speaker turns,
// returning them sorted by start with "Person N" labels numbered by first
// appearance, like session.Diarize.
func (p *Prepared) Diarize(params Params) ([]session.DiarizeSegment, map[int]string) {
	if len(p.Embeddings) == 0 {
		return nil, map[int]string{}
	}
	clusters := p.Cluster(params.Threshold, params.NumClusters)
	if params.MergeSimilarity > 0 {
		clusters = p.MergeClusters(clusters, params.MergeSimilarity)
	}
	return p.Reconstruct(clusters, params)
}

// Cluster assigns each embedding a cluster (see Params.Threshold). The
// steps of Diarize are exposed separately so a settings sweep can cluster
// once per threshold and reuse the result for every other setting.
func (p *Prepared) Cluster(threshold float64, numClusters int) []int {
	return clusterComplete(p.Embeddings, threshold, numClusters)
}

// MergeClusters merges clusters by mean embedding (see
// Params.MergeSimilarity).
func (p *Prepared) MergeClusters(clusters []int, minSimilarity float64) []int {
	if len(clusters) == 0 {
		return clusters
	}
	return mergeByCentroid(p.Embeddings, clusters, minSimilarity)
}

// Reconstruct turns cluster assignments into speaker turns, using params'
// SpeakerCountRounding, MinDurationOn and MinDurationOff.
func (p *Prepared) Reconstruct(clusters []int, params Params) ([]session.DiarizeSegment, map[int]string) {
	if len(clusters) == 0 {
		return nil, map[int]string{}
	}
	return p.reconstruct(clusters, params)
}

// reconstruct turns per-window cluster assignments into global speaker
// turns (sherpa-onnx's ReLabel, ComputeSpeakerCount, FinalizeLabels and
// ComputeResult).
func (p *Prepared) reconstruct(clusters []int, params Params) ([]session.DiarizeSegment, map[int]string) {
	return renumber(p.turns(clusters, params))
}

// ReconstructClusters is Reconstruct with each turn's Speaker left as its
// cluster index (no renumbering), for callers that track clusters across
// reclusterings (see StableLabels).
func (p *Prepared) ReconstructClusters(clusters []int, params Params) []session.DiarizeSegment {
	if len(clusters) == 0 {
		return nil
	}
	return p.turns(clusters, params)
}

// turns turns per-window cluster assignments into speaker turns, Speaker
// being the cluster index.
func (p *Prepared) turns(clusters []int, params Params) []session.DiarizeSegment {
	m := p.Meta
	numClusters := 0
	for _, c := range clusters {
		numClusters = max(numClusters, c+1)
	}
	toCluster := make(map[ChunkSpeaker]int, len(p.Pairs))
	for i, pair := range p.Pairs {
		toCluster[pair] = clusters[i]
	}
	numChunks := len(p.Labels)
	numFrames := (m.WindowSize+(numChunks-1)*m.WindowShift)/m.ReceptiveFieldShift + 1
	count := make([][]int32, numFrames) // [frame][cluster]
	for i := range count {
		count[i] = make([]int32, numClusters)
	}
	activeSum := make([]float32, numFrames) // speakers per frame, summed over windows
	weight := make([]float32, numFrames)
	for c, chunk := range p.Labels {
		start := int(float32(c)*float32(m.WindowShift)/float32(m.ReceptiveFieldShift) + 0.5)
		for f, row := range chunk {
			g := start + f
			if g >= numFrames {
				break
			}
			weight[g]++
			for spk, v := range row {
				if v == 0 {
					continue
				}
				activeSum[g]++
				if cl, ok := toCluster[ChunkSpeaker{c, spk}]; ok {
					count[g][cl]++
				}
			}
		}
	}
	lastFrame := numFrames
	if numChunks > 1 && (p.NumSamples-m.WindowSize)%m.WindowShift > 0 {
		lastFrame = min(numFrames, p.NumSamples/m.ReceptiveFieldShift+1)
	}

	rounding := params.SpeakerCountRounding
	if rounding <= 0 {
		rounding = 0.5
	}
	active := make([][]bool, numClusters) // [cluster][frame]
	for k := range active {
		active[k] = make([]bool, lastFrame)
	}
	order := make([]int, numClusters)
	for f := 0; f < lastFrame; f++ {
		n := int(activeSum[f]/(weight[f]+1e-12) + float32(1-rounding))
		if n == 0 {
			continue
		}
		for k := range order {
			order[k] = k
		}
		row := count[f]
		var votes int32
		for _, v := range row {
			votes += v
		}
		if votes == 0 {
			// Every window covering this frame was left unfingerprinted
			// (EveryNth, or the newest frames of a recluster): nothing
			// says who is speaking, and the sort below would hand it to
			// cluster 0, an arbitrary speaker. The frame stays unlabeled
			// until a later recluster sees a fingerprint for it.
			continue
		}
		sort.SliceStable(order, func(a, b int) bool { return row[order[a]] > row[order[b]] })
		for _, k := range order[:min(n, numClusters)] {
			active[k][f] = true
		}
	}

	scale := float64(m.ReceptiveFieldShift) / float64(m.SampleRate)
	offset := 0.5 * float64(m.ReceptiveFieldSize) / float64(m.SampleRate)
	var segs []session.DiarizeSegment
	for k := 0; k < numClusters; k++ {
		var turns []session.DiarizeSegment
		start := -1
		if lastFrame > 0 && active[k][0] {
			start = 0
		}
		for f := 1; f < lastFrame; f++ {
			if start >= 0 && !active[k][f] {
				turns = append(turns, session.DiarizeSegment{Start: float64(start)*scale + offset, End: float64(f)*scale + offset, Speaker: k})
				start = -1
			} else if start < 0 && active[k][f] {
				start = f
			}
		}
		if start >= 0 {
			turns = append(turns, session.DiarizeSegment{Start: float64(start)*scale + offset, End: float64(lastFrame-1)*scale + offset, Speaker: k})
		}
		// Join turns separated by a gap under MinDurationOff, then drop
		// those still under MinDurationOn.
		var joined []session.DiarizeSegment
		for _, t := range turns {
			if n := len(joined); n > 0 && t.Start-joined[n-1].End <= params.MinDurationOff {
				joined[n-1].End = t.End
				continue
			}
			joined = append(joined, t)
		}
		for _, t := range joined {
			if t.End-t.Start > params.MinDurationOn {
				segs = append(segs, t)
			}
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	return segs
}

// renumber numbers segs' clusters by first appearance, so labels read
// Person 1, 2...
func renumber(segs []session.DiarizeSegment) ([]session.DiarizeSegment, map[int]string) {
	renum := map[int]int{}
	labels := map[int]string{}
	for i := range segs {
		id, ok := renum[segs[i].Speaker]
		if !ok {
			id = len(renum)
			renum[segs[i].Speaker] = id
			labels[id] = fmt.Sprintf("Person %d", id+1)
		}
		segs[i].Speaker = id
	}
	return segs, labels
}
