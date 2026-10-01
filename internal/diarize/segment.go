package diarize

import (
	"fmt"
	"strconv"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// Meta is the segmentation model's framing, read from its ONNX metadata
// (as sherpa-onnx does): it analyzes WindowSize samples at a time, sliding
// by WindowShift, and outputs one frame per ReceptiveFieldShift samples,
// each a powerset class over NumSpeakers local speakers.
type Meta struct {
	SampleRate          int
	WindowSize          int
	WindowShift         int
	ReceptiveFieldSize  int
	ReceptiveFieldShift int
	NumSpeakers         int
	NumClasses          int
	PowersetMaxClasses  int
}

// segmenter runs the pyannote segmentation model.
type segmenter struct {
	sess     *ort.DynamicAdvancedSession
	meta     Meta
	powerset [][]int8 // class -> which local speakers are active
}

func newSegmenter(modelPath string, threads int) (*segmenter, error) {
	if err := initORT(); err != nil {
		return nil, err
	}
	md, err := ort.GetModelMetadata(modelPath)
	if err != nil {
		return nil, fmt.Errorf("reading segmentation model metadata: %w", err)
	}
	defer md.Destroy()
	read := func(key string) (int, error) {
		v, ok, err := md.LookupCustomMetadataMap(key)
		if err != nil || !ok {
			return 0, fmt.Errorf("segmentation model metadata %q missing", key)
		}
		return strconv.Atoi(v)
	}
	var m Meta
	for key, dst := range map[string]*int{
		"sample_rate": &m.SampleRate, "window_size": &m.WindowSize,
		"receptive_field_size": &m.ReceptiveFieldSize, "receptive_field_shift": &m.ReceptiveFieldShift,
		"num_speakers": &m.NumSpeakers, "num_classes": &m.NumClasses, "powerset_max_classes": &m.PowersetMaxClasses,
	} {
		if *dst, err = read(key); err != nil {
			return nil, err
		}
	}
	m.WindowShift = int(0.1 * float64(m.WindowSize)) // as sherpa-onnx

	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil || len(inputs) != 1 || len(outputs) != 1 {
		return nil, fmt.Errorf("segmentation model: expected one input and one output (%v)", err)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	if threads > 0 {
		_ = opts.SetIntraOpNumThreads(threads)
		_ = opts.SetInterOpNumThreads(1)
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath, []string{inputs[0].Name}, []string{outputs[0].Name}, opts)
	if err != nil {
		return nil, fmt.Errorf("loading segmentation model: %w", err)
	}
	s := &segmenter{sess: sess, meta: m}
	if s.powerset, err = powersetMapping(m.NumClasses, m.NumSpeakers, m.PowersetMaxClasses); err != nil {
		sess.Destroy()
		return nil, err
	}
	return s, nil
}

func (s *segmenter) close() { s.sess.Destroy() }

// powersetMapping maps each powerset class to its active local speakers:
// class 0 is silence, then each speaker alone, then each pair (see
// pyannote's Powerset and sherpa-onnx's InitPowersetMapping).
func powersetMapping(numClasses, numSpeakers, maxClasses int) ([][]int8, error) {
	m := make([][]int8, numClasses)
	for i := range m {
		m[i] = make([]int8, numSpeakers)
	}
	k := 1
	for size := 1; size <= maxClasses; size++ {
		switch size {
		case 1:
			for j := 0; j < numSpeakers; j++ {
				m[k][j] = 1
				k++
			}
		case 2:
			for j := 0; j < numSpeakers; j++ {
				for n := j + 1; n < numSpeakers; n++ {
					m[k][j], m[k][n] = 1, 1
					k++
				}
			}
		default:
			return nil, fmt.Errorf("powerset_max_classes %d not supported", maxClasses)
		}
	}
	if k != numClasses {
		return nil, fmt.Errorf("powerset mapping built %d classes, model has %d", k, numClasses)
	}
	return m, nil
}

// chunkStarts returns where each analysis window starts, and whether the
// last one is a zero-padded partial window (sherpa-onnx's chunking).
func chunkStarts(n int, m Meta) []int {
	if n <= m.WindowSize {
		return []int{0}
	}
	num := (n-m.WindowSize)/m.WindowShift + 1
	starts := make([]int, num, num+1)
	for i := range starts {
		starts[i] = i * m.WindowShift
	}
	if (n-m.WindowSize)%m.WindowShift > 0 {
		starts = append(starts, num*m.WindowShift)
	}
	return starts
}

// segment runs the model over every window of samples and returns, per
// window and output frame, which local speakers (0..NumSpeakers-1) are
// active: labels[chunk][frame][speaker] is 0 or 1. Windows are processed in
// batches, spread over workers goroutines (an ONNX Runtime session is safe
// to run concurrently).
func (s *segmenter) segment(samples []float32, workers int) ([][][]int8, error) {
	m := s.meta
	starts := chunkStarts(len(samples), m)
	labels := make([][][]int8, len(starts))
	const batch = 32
	type job struct{ from, to int }
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for w := 0; w < max(1, workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := s.runBatch(samples, starts, j.from, j.to, labels); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for from := 0; from < len(starts); from += batch {
		jobs <- job{from, min(from+batch, len(starts))}
	}
	close(jobs)
	wg.Wait()
	return labels, firstErr
}

// runBatch decodes windows [from, to) into labels.
func (s *segmenter) runBatch(samples []float32, starts []int, from, to int, labels [][][]int8) error {
	m := s.meta
	b := to - from
	in := make([]float32, b*m.WindowSize) // zero padding for a partial last window
	for i := 0; i < b; i++ {
		st := starts[from+i]
		copy(in[i*m.WindowSize:(i+1)*m.WindowSize], samples[st:min(st+m.WindowSize, len(samples))])
	}
	x, err := ort.NewTensor(ort.NewShape(int64(b), 1, int64(m.WindowSize)), in)
	if err != nil {
		return err
	}
	defer x.Destroy()
	outputs := []ort.Value{nil}
	if err := s.sess.Run([]ort.Value{x}, outputs); err != nil {
		return fmt.Errorf("segmentation: %w", err)
	}
	defer outputs[0].Destroy()
	y, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return fmt.Errorf("segmentation: unexpected output type %T", outputs[0])
	}
	shape := y.GetShape() // (batch, frames, classes)
	frames, classes := int(shape[1]), int(shape[2])
	data := y.GetData()
	for i := 0; i < b; i++ {
		chunk := make([][]int8, frames)
		for f := 0; f < frames; f++ {
			row := data[(i*frames+f)*classes : (i*frames+f+1)*classes]
			best := 0
			for c := 1; c < classes; c++ {
				if row[c] > row[best] {
					best = c
				}
			}
			chunk[f] = s.powerset[best]
		}
		labels[from+i] = chunk
	}
	return nil
}
