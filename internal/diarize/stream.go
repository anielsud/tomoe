package diarize

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
)

// StreamConfig configures a Stream.
type StreamConfig struct {
	SegmentationModel string
	EmbeddingModel    string
	// Threads for the segmentation model (the embedder uses one). Above
	// 1, the extra threads are ONNX Runtime's and don't share the Stream
	// thread's low priority.
	Threads int
	// Stride embeds every nth window; the others still count how many
	// people are talking (see Prepared.EveryNth). 0 or 1 embeds all.
	Stride int
	// ReclusterSeconds is how much new audio triggers a recluster.
	ReclusterSeconds float64
	Params           Params
	// OnTimeline, if set, receives each recluster's timeline. Called on
	// the Stream's own goroutine; it must not call back into the Stream.
	OnTimeline func(Timeline)
}

// Timeline is who spoke when, as of a recluster. Speakers are stable IDs
// (see StableLabels): a speaker keeps their number across reclusters.
type Timeline struct {
	Turns  []session.DiarizeSegment
	Labels map[int]string // stable ID -> "Person N"
	// Through is how far into the audio (seconds) the timeline covers.
	Through float64
	Final   bool
}

// Stream diarizes audio as it arrives: segmentation and embeddings for
// each analysis window once it's complete, a recluster every
// ReclusterSeconds of new audio, and a final one when Finish is called.
// The same steps as Prepare and Diarize, spread over the recording (see
// docs/speaker-pipeline-design.md). Feed never blocks on the work, which
// runs on the Stream's goroutine and may fall behind and catch up.
type Stream struct {
	cfg  StreamConfig
	seg  *segmenter
	emb  *speaker.Embedder
	meta Meta

	mu       sync.Mutex
	buf      []float32 // audio not yet past every window, from bufStart
	bufStart int
	fed      int
	finished bool
	wake     chan struct{}
	done     chan struct{}
	final    Timeline
	err      error

	// Worker-owned.
	labels      [][][]int8
	pairs       []ChunkSpeaker
	embs        [][]float32
	stable      *StableLabels
	sinceRecl   int // windows since the last recluster
	reclEvery   int
	processedTo int // next window index
}

// NewStream loads the models and starts the Stream's goroutine.
func NewStream(cfg StreamConfig) (*Stream, error) {
	seg, err := newSegmenter(cfg.SegmentationModel, max(1, cfg.Threads))
	if err != nil {
		return nil, err
	}
	emb, err := speaker.NewEmbedder(cfg.EmbeddingModel)
	if err != nil {
		seg.close()
		return nil, fmt.Errorf("loading speaker embedding model: %w", err)
	}
	s := &Stream{
		cfg: cfg, seg: seg, emb: emb, meta: seg.meta,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		stable: NewStableLabels(),
	}
	s.reclEvery = max(1, int(cfg.ReclusterSeconds*float64(s.meta.SampleRate)/float64(s.meta.WindowShift)))
	if s.cfg.Stride < 1 {
		s.cfg.Stride = 1
	}
	go s.run()
	return s, nil
}

// Feed appends 16 kHz mono audio. Safe to call from any one goroutine.
func (s *Stream) Feed(samples []float32) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.buf = append(s.buf, samples...)
	s.fed += len(samples)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Finish processes the rest of the audio, reclusters it all once more and
// returns that final timeline. Feed must not be called after it.
func (s *Stream) Finish() (Timeline, error) {
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
	return s.final, s.err
}

// Prepared returns everything computed, for saving with the session (so
// the final clustering can be rerun with other settings). Only valid
// after Finish.
func (s *Stream) Prepared() *Prepared {
	return &Prepared{Meta: s.meta, NumSamples: s.fed, Labels: s.labels, Pairs: s.pairs, Embeddings: s.embs}
}

// Close releases the models. Call after Finish.
func (s *Stream) Close() {
	s.seg.close()
	s.emb.Close()
}

func (s *Stream) run() {
	defer close(s.done)
	// One low-priority OS thread does all of it (the models run with one
	// thread each), so the meeting's audio and transcript come first.
	runtime.LockOSThread()
	lowerThreadPriority()
	m := s.meta
	for range s.wake {
		for {
			s.mu.Lock()
			start := s.processedTo * m.WindowShift
			ready := start+m.WindowSize <= s.fed
			finished := s.finished
			var window []float32
			if ready {
				window = append([]float32(nil), s.buf[start-s.bufStart:start-s.bufStart+m.WindowSize]...)
			}
			s.mu.Unlock()
			if !ready {
				if finished {
					s.finish()
					return
				}
				break
			}
			s.processWindow(window)
			s.mu.Lock()
			if drop := s.processedTo*m.WindowShift - s.bufStart; drop > 0 {
				s.buf = s.buf[drop:]
				s.bufStart += drop
			}
			s.mu.Unlock()
			if s.sinceRecl >= s.reclEvery*reclusterSpacing(len(s.embs)) {
				s.recluster(false)
			}
		}
	}
}

// processWindow segments the next window and embeds its speakers.
func (s *Stream) processWindow(window []float32) {
	c := s.processedTo
	s.processedTo++
	s.sinceRecl++
	lab := make([][][]int8, 1)
	if err := s.seg.runBatch(window, []int{0}, 0, 1, lab); err != nil {
		lab[0] = nil
		fmt.Printf("diarize: window %d: %v\n", c, err)
	}
	s.labels = append(s.labels, lab[0])
	if lab[0] == nil || c%s.cfg.Stride != 0 {
		return
	}
	pairs, ranges := speakerSampleRanges(lab, s.meta)
	var buf []float32
	for i, rs := range ranges {
		buf = buf[:0]
		for _, r := range rs {
			buf = append(buf, window[r[0]:min(r[1], len(window))]...)
		}
		v, err := s.emb.Extract(buf)
		if err != nil || !normalize(v) {
			continue
		}
		s.pairs = append(s.pairs, ChunkSpeaker{c, pairs[i].Speaker})
		s.embs = append(s.embs, v)
	}
}

// finish processes the zero-padded partial last window, if any (as
// chunkStarts does), and reclusters everything.
func (s *Stream) finish() {
	m := s.meta
	s.mu.Lock()
	start := s.processedTo * m.WindowShift
	partial := s.fed > start && (s.processedTo == 0 || s.fed > (s.processedTo-1)*m.WindowShift+m.WindowSize)
	var window []float32
	if partial {
		window = make([]float32, m.WindowSize)
		copy(window, s.buf[start-s.bufStart:])
	}
	s.mu.Unlock()
	if partial {
		s.processWindow(window)
	}
	s.final = s.recluster(true)
}

// Clustering time grows with the square of the number of embeddings
// (about 0.7 s for 2,400 here, an hour at every 2nd window). Past
// spaceAbove, reclusters are spaced out by the same factor, so their CPU
// cost per minute stays flat as a long meeting grows. Every clustering
// keeps at most maxCluster embeddings, thinned evenly across the meeting:
// the comparison table takes 4 bytes per pair, 128 MB at 8,000 (about 3.3
// hours at every 2nd window).
const (
	spaceAbove = 3000
	maxCluster = 8000
)

// reclusterSpacing is how many recluster intervals to wait at n
// embeddings.
func reclusterSpacing(n int) int {
	if n <= spaceAbove {
		return 1
	}
	r := float64(n) / spaceAbove
	return int(r*r + 0.5)
}

// thin keeps at most limit of pairs/embs, evenly spaced.
func thin(pairs []ChunkSpeaker, embs [][]float32, limit int) ([]ChunkSpeaker, [][]float32) {
	if len(embs) <= limit {
		return pairs, embs
	}
	outP := make([]ChunkSpeaker, 0, limit)
	outE := make([][]float32, 0, limit)
	for i := 0; i < limit; i++ {
		j := i * len(embs) / limit
		outP = append(outP, pairs[j])
		outE = append(outE, embs[j])
	}
	return outP, outE
}

// recluster clusters everything so far and reports the timeline.
func (s *Stream) recluster(final bool) Timeline {
	s.sinceRecl = 0
	m := s.meta
	n := len(s.labels)
	numSamples := 0
	if n > 0 {
		numSamples = (n-1)*m.WindowShift + m.WindowSize
	}
	if final {
		numSamples = s.fed
	}
	tl := Timeline{Through: float64(numSamples) / float64(m.SampleRate), Final: final, Labels: map[int]string{}}
	if len(s.embs) > 0 {
		pairs, embs := thin(s.pairs, s.embs, maxCluster)
		p := &Prepared{Meta: m, NumSamples: numSamples, Labels: s.labels, Pairs: pairs, Embeddings: embs}
		clusters := p.Cluster(s.cfg.Params.Threshold, s.cfg.Params.NumClusters)
		if s.cfg.Params.MergeSimilarity > 0 {
			clusters = p.MergeClusters(clusters, s.cfg.Params.MergeSimilarity)
		}
		ids := s.stable.Assign(pairs, clusters)
		for _, t := range p.ReconstructClusters(clusters, s.cfg.Params) {
			t.Speaker = ids[t.Speaker]
			tl.Turns = append(tl.Turns, t)
			tl.Labels[t.Speaker] = fmt.Sprintf("Person %d", t.Speaker+1)
		}
	}
	if s.cfg.OnTimeline != nil {
		s.cfg.OnTimeline(tl)
	}
	return tl
}
