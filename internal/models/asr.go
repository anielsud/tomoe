package models

import (
	"fmt"
	"os"
	"path/filepath"
)

// ASRModel is a transcription model in one archive directory. The first
// is the original, multilingual Parakeet: every install has it and every
// language without a better model uses it.
type ASRModel struct {
	ID     string // config value, e.g. "parakeet-v2-en"
	Name   string // shown in Tools
	Kind   string // ASRKindTransducer or ASRKindCohere: which engine loads it
	Subdir string // directory the archive extracts to
	URL    string
}

// Model kinds: a Parakeet-style transducer (encoder, decoder, joiner,
// tokens.txt; word timestamps) or Cohere Transcribe (encoder, decoder,
// tokens.txt; no timestamps).
const (
	ASRKindTransducer = "transducer"
	ASRKindCohere     = "cohere"
)

// Transcription model IDs. ASRModelAuto picks by language (see
// ResolveASRModel).
const (
	ASRModelAuto         = "auto"
	ASRModelParakeetV3   = "parakeet-v3"
	ASRModelParakeetV2En = "parakeet-v2-en"
	ASRModelCohere       = "cohere-transcribe"
)

// ASRModels are the selectable transcription models.
var ASRModels = []ASRModel{
	{
		ID:     ASRModelParakeetV3,
		Name:   "Parakeet TDT 0.6B v3 INT8 (25 languages)",
		Kind:   ASRKindTransducer,
		Subdir: ParakeetSubdir,
		URL:    ParakeetArchiveURL,
	},
	{
		// English only, and more accurate in English than v3: on four
		// recorded meetings it fixed about a third of v3's
		// meaning-changing errors (docs/transcription-accuracy-research.md).
		ID:     ASRModelParakeetV2En,
		Name:   "Parakeet TDT 0.6B v2 INT8 (English)",
		Kind:   ASRKindTransducer,
		Subdir: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
	},
	{
		// The most accurate in English of eleven models compared on four
		// recorded meetings: 41% of v3's judged errors fixed against 33%
		// for v2, terms and names markedly better, at about 3x v2's decode
		// time (docs/transcription-accuracy-research.md). Apache-2.0.
		ID:     ASRModelCohere,
		Name:   "Cohere Transcribe INT8 (14 languages)",
		Kind:   ASRKindCohere,
		Subdir: "sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01.tar.bz2",
	},
}

// ASRModelByID returns the model with id.
func ASRModelByID(id string) (ASRModel, bool) {
	for _, m := range ASRModels {
		if m.ID == id {
			return m, true
		}
	}
	return ASRModel{}, false
}

// ResolveASRModel is the model setting (a model ID, or "auto"/"") picks
// for lang: automatic means Cohere Transcribe for English and the
// multilingual Parakeet for everything else. An unknown setting falls back
// to automatic.
func ResolveASRModel(setting, lang string) ASRModel {
	if m, ok := ASRModelByID(setting); ok {
		return m
	}
	if lang == "" || lang == "en" {
		m, _ := ASRModelByID(ASRModelCohere)
		return m
	}
	return ASRModels[0]
}

// ASRModelDir is where model is extracted.
func (s *Status) ASRModelDir(m ASRModel) string { return filepath.Join(s.ModelDir, m.Subdir) }

// ASRModelFiles are a transducer model's encoder, decoder, joiner and
// tokens paths.
func (s *Status) ASRModelFiles(m ASRModel) (encoder, decoder, joiner, tokens string) {
	dir := s.ASRModelDir(m)
	return filepath.Join(dir, encoderFile), filepath.Join(dir, decoderFile), filepath.Join(dir, joinerFile), filepath.Join(dir, tokensFile)
}

// ASRModelReady reports whether model is downloaded.
func (s *Status) ASRModelReady(m ASRModel) bool {
	if m.Kind == ASRKindCohere {
		dir := s.ASRModelDir(m)
		return allFilesExist(filepath.Join(dir, encoderFile), filepath.Join(dir, decoderFile), filepath.Join(dir, tokensFile))
	}
	e, d, j, t := s.ASRModelFiles(m)
	return allFilesExist(e, d, j, t)
}

// ASRModelFor is the transcription model to use for lang: the one setting
// resolves to if it's downloaded, else the multilingual one every install
// has (so a missing download degrades to the previous behavior). fellBack
// reports that.
func (s *Status) ASRModelFor(setting, lang string) (m ASRModel, fellBack bool) {
	m = ResolveASRModel(setting, lang)
	if m.ID == ASRModels[0].ID || s.ASRModelReady(m) {
		return m, false
	}
	return ASRModels[0], true
}

// ASRModelsNeeded lists the models setting needs for langs beyond the
// multilingual one Download fetches.
func ASRModelsNeeded(setting string, langs []string) []ASRModel {
	var out []ASRModel
	seen := map[string]bool{ASRModels[0].ID: true}
	for _, l := range langs {
		if m := ResolveASRModel(setting, l); !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

// DownloadASRModels downloads each model ASRModelsNeeded lists that isn't
// present yet (all of them if force).
func (m *Manager) DownloadASRModels(setting string, langs []string, force bool, onProgress ProgressFunc) error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}
	status := m.Check()
	for _, am := range ASRModelsNeeded(setting, langs) {
		if !force && status.ASRModelReady(am) {
			continue
		}
		fmt.Printf("Downloading transcription model %s...\n", am.Name)
		if err := m.downloadAndExtractArchive(am.URL, am.Name, onProgress); err != nil {
			return fmt.Errorf("downloading transcription model %s: %w", am.Name, err)
		}
	}
	return nil
}
