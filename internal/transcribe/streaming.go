package transcribe

import (
	"fmt"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

// StreamingEngine produces incrementally-updating partial transcripts as
// audio arrives, for realtime display — distinct from Engine, whose
// TranscribeDirect/TranscribeSamples are one-shot calls over already-complete
// audio. This is "pass 1" of the live pipeline's two-pass transcription
// (see internal/live): low latency, not necessarily the most accurate
// final text — internal/live re-decodes each finished segment through an
// Engine (Parakeet, offline, higher-quality decode settings) as "pass 2"
// and replaces the pass-1 text once that's ready.
type StreamingEngine interface {
	// NewSession starts a new incremental decode session. Callers create
	// one per audio source and keep it for that source's lifetime,
	// calling Reset between utterances rather than creating a new
	// session each time (session creation is comparatively expensive;
	// Reset is not).
	NewSession() (StreamingSession, error)
	Close()
}

// StreamingSession is stateful: Feed is meant to be called repeatedly
// with small, sequential chunks of the same audio stream (e.g. once per
// VAD window) as they arrive. Not safe for concurrent use — one
// goroutine, matching how internal/live's per-source pipeline already
// runs single-threaded.
type StreamingSession interface {
	// Feed appends samples and returns the current (possibly partial)
	// hypothesis text for the utterance in progress since the last
	// Reset. Safe to call with small chunks (e.g. one VAD window) —
	// internally batches until the underlying model has enough audio for
	// another decode step.
	Feed(samples []float32) (text string, err error)
	// Reset clears decode state and starts a fresh utterance — call this
	// when the caller's own segmentation (e.g. VAD) decides the current
	// utterance is done, so the next Feed starts clean rather than
	// accumulating unbounded context across an entire session.
	Reset()
	Close()
}

// zipformerStreamingEngine implements StreamingEngine using sherpa-onnx's
// online (streaming) Zipformer transducer recognizer.
type zipformerStreamingEngine struct {
	recognizer *sherpa.OnlineRecognizer
}

// StreamingConfig holds paths for the streaming (online) transducer model.
type StreamingConfig struct {
	EncoderPath string
	DecoderPath string
	JoinerPath  string
	TokensPath  string
	NumThreads  int
}

// NewStreamingEngine creates a StreamingEngine from a streaming Zipformer
// transducer model (encoder/decoder/joiner + tokens).
func NewStreamingEngine(cfg StreamingConfig) (StreamingEngine, error) {
	numThreads := cfg.NumThreads
	if numThreads <= 0 {
		numThreads = 2
	}

	config := &sherpa.OnlineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{
			SampleRate: sampleRate,
			FeatureDim: 80,
		},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: cfg.EncoderPath,
				Decoder: cfg.DecoderPath,
				Joiner:  cfg.JoinerPath,
			},
			Tokens:     cfg.TokensPath,
			NumThreads: numThreads,
			Provider:   "cpu",
			ModelType:  "zipformer2",
		},
		DecodingMethod: "greedy_search",
		// Endpoint detection is deliberately off: internal/live already
		// has its own segmentation (Silero VAD) that decides when an
		// utterance is "done" and calls Reset accordingly. Running a
		// second, independent endpoint detector here would just be a
		// competing opinion with no consumer.
		EnableEndpoint: 0,
	}

	recognizer := sherpa.NewOnlineRecognizer(config)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create streaming recognizer (check model paths)")
	}
	sigfix.AfterSherpa()

	return &zipformerStreamingEngine{recognizer: recognizer}, nil
}

func (e *zipformerStreamingEngine) NewSession() (StreamingSession, error) {
	stream := sherpa.NewOnlineStream(e.recognizer)
	if stream == nil {
		return nil, fmt.Errorf("failed to create online stream")
	}
	return &zipformerStreamingSession{recognizer: e.recognizer, stream: stream}, nil
}

func (e *zipformerStreamingEngine) Close() {
	if e.recognizer != nil {
		sherpa.DeleteOnlineRecognizer(e.recognizer)
		e.recognizer = nil
	}
}

// zipformerStreamingSession wraps one OnlineStream. mu guards against the
// theoretical case of Feed/Reset/Close racing (the interface's contract
// says single-goroutine use, but native resource cleanup — Close — is
// cheap to make safe against a stray concurrent call, so it is).
type zipformerStreamingSession struct {
	mu         sync.Mutex
	recognizer *sherpa.OnlineRecognizer
	stream     *sherpa.OnlineStream
	closed     bool
}

func (s *zipformerStreamingSession) Feed(samples []float32) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || len(samples) == 0 {
		return "", nil
	}

	s.stream.AcceptWaveform(sampleRate, samples)
	for s.recognizer.IsReady(s.stream) {
		s.recognizer.Decode(s.stream)
	}

	result := s.recognizer.GetResult(s.stream)
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Text), nil
}

func (s *zipformerStreamingSession) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.recognizer.Reset(s.stream)
}

func (s *zipformerStreamingSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	sherpa.DeleteOnlineStream(s.stream)
	s.closed = true
}
