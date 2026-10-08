package models

// LiveModel is a streaming English model for the live ("pass 1") text:
// what shows while people speak, before the turn's final text replaces it.
type LiveModel struct {
	ID, Name                         string
	URL                              string // archive
	Subdir                           string // directory after extraction
	Encoder, Decoder, Joiner, Tokens string
	// ModelType tells sherpa-onnx how to load it ("" lets it read the
	// model's own metadata).
	ModelType string
}

const (
	LiveFastConformer = "fastconformer-480"
	LiveZipformer     = "zipformer-2023"
	LiveNemotron      = "nemotron-560"
)

// LiveModels are the live models a meeting can use. On about 8 minutes of
// two meetings, against the final text (Cohere): the LibriSpeech Zipformer
// differed on 41% / 52% of words, NeMo's streaming FastConformer on 25% /
// 38% for the same CPU, Nemotron on 18% / 31% for 3x the CPU and ~1.9 GB
// (docs/transcription-accuracy-research.md).
var LiveModels = []LiveModel{
	{
		ID: LiveFastConformer, Name: "NeMo streaming FastConformer (480 ms)",
		URL:     "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-480ms-int8.tar.bz2",
		Subdir:  "sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-480ms-int8",
		Encoder: "encoder.int8.onnx", Decoder: "decoder.int8.onnx", Joiner: "joiner.int8.onnx", Tokens: "tokens.txt",
	},
	{
		ID: LiveZipformer, Name: "Streaming Zipformer (LibriSpeech, 2023)",
		URL:     EnglishStreamingArchiveURL,
		Subdir:  EnglishStreamingSubdir,
		Encoder: englishStreamingEncoderFile, Decoder: englishStreamingDecoderFile, Joiner: englishStreamingJoinerFile, Tokens: englishStreamingTokensFile,
		ModelType: "zipformer2",
	},
	{
		ID: LiveNemotron, Name: "Nemotron Speech Streaming 0.6B (560 ms)",
		URL:     "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemotron-speech-streaming-en-0.6b-560ms-int8-2026-04-25.tar.bz2",
		Subdir:  "sherpa-onnx-nemotron-speech-streaming-en-0.6b-560ms-int8-2026-04-25",
		Encoder: "encoder.int8.onnx", Decoder: "decoder.int8.onnx", Joiner: "joiner.int8.onnx", Tokens: "tokens.txt",
	},
}

// ResolveLiveModel is the live model for the live_model setting: "" or
// "auto" is the FastConformer; an unknown ID falls back to it too.
func ResolveLiveModel(id string) LiveModel {
	for _, m := range LiveModels {
		if m.ID == id {
			return m
		}
	}
	return LiveModels[0]
}
