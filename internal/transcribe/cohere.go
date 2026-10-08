package transcribe

import (
	"fmt"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

// NewCohereEngine loads Cohere Transcribe (encoder.int8.onnx,
// decoder.int8.onnx and tokens.txt in dir) for lang. It returns no token
// timestamps; the live pipeline estimates word timings from the text (see
// session.SpreadWords). It takes no hotwords.
func NewCohereEngine(dir, lang string, threads int, vadPath string) (Engine, error) {
	if threads <= 0 {
		threads = 4
	}
	if lang == "" {
		lang = "en"
	}
	rec := sherpa.NewOfflineRecognizer(&sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OfflineModelConfig{
			CohereTranscribe: sherpa.OfflineCohereTranscribeModelConfig{
				Encoder:                     filepath.Join(dir, "encoder.int8.onnx"),
				Decoder:                     filepath.Join(dir, "decoder.int8.onnx"),
				Language:                    lang,
				UsePunct:                    1,
				UseInverseTextNormalization: 1,
			},
			Tokens:     filepath.Join(dir, "tokens.txt"),
			NumThreads: threads,
			Provider:   "cpu",
		},
		DecodingMethod: "greedy_search",
	})
	if rec == nil {
		return nil, fmt.Errorf("failed to load Cohere Transcribe from %s", dir)
	}
	sigfix.AfterSherpa()
	return &parakeetEngine{recognizer: rec, vadConfig: vadModelConfig(vadPath)}, nil
}
