package transcribe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

// CandidateKinds are the offline model families NewCandidateEngine can
// load, for comparing transcription models on real meetings (tomoe session
// replay --asr-kind).
var CandidateKinds = []string{"transducer", "whisper", "canary", "moonshine", "qwen3", "funasr-nano", "cohere"}

// NewCandidateEngine loads the model in dir as an Engine. kind says which
// family it is; the files are found by name (int8 preferred).
func NewCandidateEngine(kind, dir string, threads int) (Engine, error) {
	files, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	pick := func(parts ...string) string { return pickFile(files, parts...) }
	mc := sherpa.OfflineModelConfig{NumThreads: max(1, threads), Provider: "cpu", Tokens: pick("tokens.txt")}
	switch kind {
	case "transducer":
		mc.Transducer = sherpa.OfflineTransducerModelConfig{Encoder: pick("encoder", ".onnx"), Decoder: pick("decoder", ".onnx"), Joiner: pick("joiner", ".onnx")}
		mc.ModelType = "nemo_transducer"
	case "whisper":
		mc.Whisper = sherpa.OfflineWhisperModelConfig{Encoder: pick("encoder", ".onnx"), Decoder: pick("decoder", ".onnx"), Language: "en", Task: "transcribe", TailPaddings: -1}
	case "canary":
		mc.Canary = sherpa.OfflineCanaryModelConfig{Encoder: pick("encoder", ".onnx"), Decoder: pick("decoder", ".onnx"), SrcLang: "en", TgtLang: "en", UsePnc: 1}
	case "moonshine":
		mc.Moonshine = sherpa.OfflineMoonshineModelConfig{
			Preprocessor: pick("preprocess"), Encoder: pick("encode"),
			UncachedDecoder: pick("uncached"), CachedDecoder: pick("cached_decode"), MergedDecoder: pick("merged"),
		}
	case "qwen3":
		mc.Qwen3ASR = sherpa.OfflineQwen3ASRModelConfig{
			ConvFrontend: pick("conv"), Encoder: pick("encoder", ".onnx"), Decoder: pick("decoder", ".onnx"),
			Tokenizer: pickDir(dir, "tokenizer"), MaxNewTokens: 512,
		}
	case "funasr-nano":
		mc.FunAsrNano = sherpa.OfflineFunASRNanoModelConfig{
			EncoderAdaptor: pick("encoder", ".onnx"), LLM: pick("llm", ".onnx"), Embedding: pick("embedding", ".onnx"),
			Tokenizer: pickDir(dir, "tokenizer", "Qwen"), MaxNewTokens: 512, Language: "en",
		}
	case "cohere":
		mc.CohereTranscribe = sherpa.OfflineCohereTranscribeModelConfig{Encoder: pick("encoder", ".onnx"), Decoder: pick("decoder", ".onnx"), Language: "en", UsePunct: 1, UseInverseTextNormalization: 1}
	default:
		return nil, fmt.Errorf("unknown model kind %q (one of %s)", kind, strings.Join(CandidateKinds, ", "))
	}
	rec := sherpa.NewOfflineRecognizer(&sherpa.OfflineRecognizerConfig{
		FeatConfig:     sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig:    mc,
		DecodingMethod: "greedy_search",
	})
	if rec == nil {
		return nil, fmt.Errorf("couldn't load %s model from %s (files found: %d)", kind, dir, len(files))
	}
	sigfix.AfterSherpa()
	return &parakeetEngine{recognizer: rec}, nil
}

// listFiles lists the files in dir and one level below.
func listFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, dir), string(filepath.Separator)) > 1 {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// pickFile is the file whose name contains every part, preferring an int8
// one, or "".
func pickFile(files []string, parts ...string) string {
	var best string
	for _, f := range files {
		name := strings.ToLower(filepath.Base(f))
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(name, strings.ToLower(p))
		}
		if !ok {
			continue
		}
		if best == "" || (strings.Contains(name, "int8") && !strings.Contains(strings.ToLower(filepath.Base(best)), "int8")) {
			best = f
		}
	}
	return best
}

// pickDir is the first directory under dir whose name contains any of
// names, or "".
func pickDir(dir string, names ...string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		for _, n := range names {
			if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), strings.ToLower(n)) {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}
