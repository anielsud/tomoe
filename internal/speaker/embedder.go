package speaker

import (
	"fmt"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

const embeddingSampleRate = 16000

// Embedder extracts speaker embeddings from audio samples using sherpa-onnx.
type Embedder struct {
	extractor *sherpa.SpeakerEmbeddingExtractor
}

// NewEmbedder creates a speaker embedding extractor from the given model file.
func NewEmbedder(modelPath string) (*Embedder, error) {
	config := &sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      modelPath,
		NumThreads: 1,
		Provider:   "cpu",
	}

	extractor := sherpa.NewSpeakerEmbeddingExtractor(config)
	if extractor == nil {
		return nil, fmt.Errorf("failed to create speaker embedding extractor (check model path: %s)", modelPath)
	}
	sigfix.AfterSherpa()

	return &Embedder{
		extractor: extractor,
	}, nil
}

// Extract computes a speaker embedding from audio samples (16kHz mono float32).
func (e *Embedder) Extract(samples []float32) ([]float32, error) {
	if len(samples) == 0 {
		return nil, fmt.Errorf("empty audio samples")
	}

	stream := e.extractor.CreateStream()
	if stream == nil {
		return nil, fmt.Errorf("failed to create embedding stream")
	}
	defer sherpa.DeleteOnlineStream(stream)

	stream.AcceptWaveform(embeddingSampleRate, samples)
	stream.InputFinished()

	if !e.extractor.IsReady(stream) {
		return nil, fmt.Errorf("not enough audio for speaker embedding")
	}

	embedding := e.extractor.Compute(stream)
	if len(embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	return embedding, nil
}

// Dim returns the embedding dimension.
func (e *Embedder) Dim() int {
	return e.extractor.Dim()
}

// Close releases the underlying native resources.
func (e *Embedder) Close() {
	if e.extractor != nil {
		sherpa.DeleteSpeakerEmbeddingExtractor(e.extractor)
		e.extractor = nil
	}
}

// EmbedderSet holds an Embedder per model file, loaded on first use, so
// each meeting can use the speaker model chosen for its language.
type EmbedderSet struct {
	mu     sync.Mutex
	byPath map[string]*Embedder
}

// NewEmbedderSet returns an empty set.
func NewEmbedderSet() *EmbedderSet {
	return &EmbedderSet{byPath: map[string]*Embedder{}}
}

// Get returns the Embedder for modelPath, loading it if needed.
func (s *EmbedderSet) Get(modelPath string) (*Embedder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.byPath[modelPath]; ok {
		return e, nil
	}
	e, err := NewEmbedder(modelPath)
	if err != nil {
		return nil, err
	}
	s.byPath[modelPath] = e
	return e, nil
}

// Len is how many models are loaded.
func (s *EmbedderSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byPath)
}

// Close releases every loaded Embedder.
func (s *EmbedderSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, e := range s.byPath {
		e.Close()
		delete(s.byPath, p)
	}
}
