package transcribe

import (
	"fmt"
	"os"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

const sampleRate = 16000

// parakeetEngine implements Engine using sherpa-onnx offline recognition.
type parakeetEngine struct {
	recognizer *sherpa.OfflineRecognizer
	vadConfig  *sherpa.VadModelConfig
}

// NewEngine creates a transcription engine with the given config.
func NewEngine(cfg Config) (Engine, error) {
	provider := "cpu"
	numThreads := cfg.NumThreads
	if numThreads <= 0 {
		numThreads = 4
	}

	if cfg.UseGPU {
		provider = "cuda"
		numThreads = 1
	}

	decodingMethod := "greedy_search"
	if cfg.DecodingMethod != "" {
		decodingMethod = cfg.DecodingMethod
	}

	recognizerConfig := &sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{
			SampleRate: sampleRate,
			FeatureDim: 80,
		},
		ModelConfig: sherpa.OfflineModelConfig{
			Transducer: sherpa.OfflineTransducerModelConfig{
				Encoder: cfg.EncoderPath,
				Decoder: cfg.DecoderPath,
				Joiner:  cfg.JoinerPath,
			},
			Tokens:     cfg.TokensPath,
			NumThreads: numThreads,
			Provider:   provider,
			ModelType:  "nemo_transducer",
		},
		DecodingMethod: decodingMethod,
		MaxActivePaths: cfg.MaxActivePaths,
		HotwordsFile:   cfg.HotwordsFile,
		HotwordsScore:  cfg.HotwordsScore,
	}

	recognizer := sherpa.NewOfflineRecognizer(recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create offline recognizer (check model paths and provider)")
	}

	// ONNX Runtime installs a SIGSEGV handler without SA_ONSTACK, which
	// conflicts with Go's signal handling. Fix it after initialization.
	sigfix.AfterSherpa()

	return &parakeetEngine{
		recognizer: recognizer,
		vadConfig:  vadModelConfig(cfg.VADPath),
	}, nil
}

// vadModelConfig is the speech detector TranscribeSamples segments with,
// or nil without a model.
func vadModelConfig(path string) *sherpa.VadModelConfig {
	if path == "" {
		return nil
	}
	return &sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              path,
			Threshold:          0.5,
			MinSilenceDuration: 0.5,
			MinSpeechDuration:  0.25,
			WindowSize:         512,
			MaxSpeechDuration:  30.0,
		},
		SampleRate: sampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}
}

// TranscribeSamples transcribes raw float32 PCM audio at 16kHz.
// Uses VAD to segment audio if configured.
func (e *parakeetEngine) TranscribeSamples(samples []float32) (*Result, error) {
	if len(samples) == 0 {
		return &Result{}, nil
	}

	duration := float64(len(samples)) / float64(sampleRate)

	// If VAD is configured, segment audio first
	if e.vadConfig != nil {
		return e.transcribeWithVAD(samples, duration)
	}

	// Direct transcription without VAD
	return e.transcribeDirect(samples, duration)
}

// TranscribeDirect transcribes pre-segmented audio without VAD.
func (e *parakeetEngine) TranscribeDirect(samples []float32) (*Result, error) {
	if len(samples) == 0 {
		return &Result{}, nil
	}
	duration := float64(len(samples)) / float64(sampleRate)
	return e.transcribeDirect(samples, duration)
}

// TranscribeFile transcribes an audio file (WAV, FLAC, or OGG).
// Non-WAV formats are converted to WAV via ffmpeg before transcription.
func (e *parakeetEngine) TranscribeFile(path string) (*Result, error) {
	if !IsSupportedFormat(path) {
		return nil, fmt.Errorf("unsupported audio format: %s", path)
	}

	wavPath := path
	if needsConversion(path) {
		converted, err := convertToWAV(path)
		if err != nil {
			return nil, fmt.Errorf("converting %s to WAV: %w", path, err)
		}
		defer func() { _ = os.Remove(converted) }()
		wavPath = converted
	}

	wave := sherpa.ReadWave(wavPath)
	if wave == nil {
		return nil, fmt.Errorf("failed to read audio file: %s", wavPath)
	}

	return e.TranscribeSamples(wave.Samples)
}

// Close releases all native resources.
func (e *parakeetEngine) Close() {
	if e.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(e.recognizer)
		e.recognizer = nil
	}
}

// transcribeDirect transcribes audio samples without VAD segmentation.
func (e *parakeetEngine) transcribeDirect(samples []float32, duration float64) (*Result, error) {
	stream := sherpa.NewOfflineStream(e.recognizer)
	if stream == nil {
		return nil, fmt.Errorf("failed to create offline stream")
	}
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(sampleRate, samples)
	e.recognizer.Decode(stream)

	r := stream.GetResult()
	if r == nil {
		return &Result{Duration: duration}, nil
	}

	return &Result{
		Text:       strings.TrimSpace(r.Text),
		Tokens:     r.Tokens,
		Timestamps: r.Timestamps,
		Duration:   duration,
		Language:   r.Lang,
	}, nil
}

// transcribeWithVAD segments audio using Silero VAD, then transcribes each segment.
func (e *parakeetEngine) transcribeWithVAD(samples []float32, duration float64) (*Result, error) {
	vad := sherpa.NewVoiceActivityDetector(e.vadConfig, 60.0)
	if vad == nil {
		return nil, fmt.Errorf("failed to create VAD (check model path)")
	}
	defer sherpa.DeleteVoiceActivityDetector(vad)
	sigfix.AfterSherpa()

	// Feed audio to VAD in windows
	windowSize := e.vadConfig.SileroVad.WindowSize
	for i := 0; i+windowSize <= len(samples); i += windowSize {
		vad.AcceptWaveform(samples[i : i+windowSize])
	}
	vad.Flush()

	// Collect all speech segments
	var texts []string
	var allTokens []string
	var allTimestamps []float32
	var lang string

	for !vad.IsEmpty() {
		segment := vad.Front()
		vad.Pop()

		if len(segment.Samples) == 0 {
			continue
		}

		stream := sherpa.NewOfflineStream(e.recognizer)
		if stream == nil {
			continue
		}

		stream.AcceptWaveform(sampleRate, segment.Samples)
		e.recognizer.Decode(stream)

		r := stream.GetResult()
		if r == nil {
			sherpa.DeleteOfflineStream(stream)
			continue
		}
		text := strings.TrimSpace(r.Text)
		if text != "" {
			texts = append(texts, text)
			allTokens = append(allTokens, r.Tokens...)
			allTimestamps = append(allTimestamps, r.Timestamps...)
			if lang == "" && r.Lang != "" {
				lang = r.Lang
			}
		}

		sherpa.DeleteOfflineStream(stream)
	}

	return &Result{
		Text:       strings.Join(texts, " "),
		Tokens:     allTokens,
		Timestamps: allTimestamps,
		Duration:   duration,
		Language:   lang,
	}, nil
}
