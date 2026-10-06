package models

import (
	"fmt"
	"os"
	"path/filepath"
)

// ASRModel is a Parakeet-style transcription model (encoder, decoder,
// joiner, tokens.txt in one archive directory). The first is the original,
// multilingual one: every install has it and every non-English language
// uses it.
type ASRModel struct {
	ID     string // config value, e.g. "parakeet-v2-en"
	Name   string // shown in Tools
	Subdir string // directory the archive extracts to
	URL    string
}

// Transcription model IDs. ASRModelAuto picks by language (see
// ResolveASRModel).
const (
	ASRModelAuto         = "auto"
	ASRModelParakeetV3   = "parakeet-v3"
	ASRModelParakeetV2En = "parakeet-v2-en"
)

// ASRModels are the selectable transcription models.
var ASRModels = []ASRModel{
	{
		ID:     ASRModelParakeetV3,
		Name:   "Parakeet TDT 0.6B v3 INT8 (25 languages)",
		Subdir: ParakeetSubdir,
		URL:    ParakeetArchiveURL,
	},
	{
		// English only, and more accurate in English: on four recorded
		// meetings it fixed about a third of v3's meaning-changing errors
		// (docs/transcription-accuracy-research.md).
		ID:     ASRModelParakeetV2En,
		Name:   "Parakeet TDT 0.6B v2 INT8 (English)",
		Subdir: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
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
// for lang: automatic means the English model for English and the
// multilingual one for everything else. An unknown setting falls back to
// automatic.
func ResolveASRModel(setting, lang string) ASRModel {
	if m, ok := ASRModelByID(setting); ok {
		return m
	}
	if lang == "" || lang == "en" {
		m, _ := ASRModelByID(ASRModelParakeetV2En)
		return m
	}
	return ASRModels[0]
}

// ASRModelFiles are model's encoder, decoder, joiner and tokens paths.
func (s *Status) ASRModelFiles(m ASRModel) (encoder, decoder, joiner, tokens string) {
	dir := filepath.Join(s.ModelDir, m.Subdir)
	return filepath.Join(dir, encoderFile), filepath.Join(dir, decoderFile), filepath.Join(dir, joinerFile), filepath.Join(dir, tokensFile)
}

// ASRModelReady reports whether model is downloaded.
func (s *Status) ASRModelReady(m ASRModel) bool {
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
