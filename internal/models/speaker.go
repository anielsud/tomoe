package models

import (
	"fmt"
	"os"
	"path/filepath"
)

// SpeakerModel is a speaker embedding model: what tells voices apart, both
// for live speaker clustering and for the post-meeting diarization pass.
// The models share an interface (16 kHz audio in, one fixed-length
// embedding out) but not a scale, so each carries the diarization
// settings tuned for it.
type SpeakerModel struct {
	ID   string // config value, e.g. "eres2net-en"
	Name string // shown in Settings and Tools
	File string // file name in the model directory
	URL  string

	// DiarizeThreshold and DiarizeMerge are the post-meeting diarization
	// clustering cut and speaker-merge similarity for this model (see
	// session.DiarizeConfig).
	DiarizeThreshold float64
	DiarizeMerge     float64

	// StreamThreshold and StreamMerge are the clustering cut and centroid
	// merge for Tomoe's own diarizer (internal/diarize), used when
	// diarizing during the meeting. Its distances aren't sherpa-onnx's
	// scale, so these differ from the above.
	StreamThreshold float64
	StreamMerge     float64
}

// Speaker model IDs. SpeakerModelAuto picks by meeting language (see
// ResolveSpeakerModel).
const (
	SpeakerModelAuto       = "auto"
	SpeakerModelEres2Net   = "eres2net-base"
	SpeakerModelEres2NetEn = "eres2net-en"
)

const speakerModelsURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/"

// SpeakerModels are the selectable speaker embedding models. The first is
// the original model, trained on Mandarin (3D-Speaker): it's what every
// non-English meeting uses.
var SpeakerModels = []SpeakerModel{
	{
		ID:               SpeakerModelEres2Net,
		Name:             "ERes2Net base (3D-Speaker, Mandarin-trained)",
		File:             SpeakerEmbeddingFile,
		URL:              SpeakerEmbeddingURL,
		DiarizeThreshold: 1.1,
		DiarizeMerge:     0.55,
		// Best on a plateau in `tomoe eval --sweep --own-diarizer`.
		StreamThreshold: 0.7,
		StreamMerge:     0.6,
	},
	{
		ID:   SpeakerModelEres2NetEn,
		Name: "ERes2Net (VoxCeleb, English-trained)",
		File: "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx",
		URL:  speakerModelsURL + "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx",
		// The base model's settings sit on a plateau for this model too
		// (`tomoe eval --embedding-model eres2net-en --sweep`; see
		// docs/speaker-attribution-research.md).
		DiarizeThreshold: 1.1,
		DiarizeMerge:     0.55,
		StreamThreshold:  0.6,
		StreamMerge:      0.5,
	},
}

// SpeakerModelByID returns the model with id.
func SpeakerModelByID(id string) (SpeakerModel, bool) {
	for _, m := range SpeakerModels {
		if m.ID == id {
			return m, true
		}
	}
	return SpeakerModel{}, false
}

// ResolveSpeakerModel is the model setting (a model ID, or "auto"/"")
// picks for a meeting in lang: automatic means the English-trained model
// for English and the base model for everything else (Bengali included,
// until a model is measured on it). An unknown setting falls back to
// automatic.
func ResolveSpeakerModel(setting, lang string) SpeakerModel {
	if m, ok := SpeakerModelByID(setting); ok {
		return m
	}
	if lang == "" || lang == "en" {
		m, _ := SpeakerModelByID(SpeakerModelEres2NetEn)
		return m
	}
	return SpeakerModels[0]
}

// SpeakerModelPath is where model lives in the model directory.
func (s *Status) SpeakerModelPath(m SpeakerModel) string {
	return filepath.Join(s.ModelDir, m.File)
}

// SpeakerModelReady reports whether model is downloaded.
func (s *Status) SpeakerModelReady(m SpeakerModel) bool {
	return fileExists(s.SpeakerModelPath(m))
}

// SpeakerModelFor is the speaker model to use for a meeting in lang, and
// its path: the one setting resolves to if it's downloaded, else the base
// model (what every install has), so a missing download degrades to the
// previous behavior instead of no speakers at all. fellBack reports that.
func (s *Status) SpeakerModelFor(setting, lang string) (m SpeakerModel, path string, fellBack bool) {
	m = ResolveSpeakerModel(setting, lang)
	if s.SpeakerModelReady(m) {
		return m, s.SpeakerModelPath(m), false
	}
	base := SpeakerModels[0]
	return base, s.SpeakerModelPath(base), m.ID != base.ID
}

// DownloadSpeakerModels downloads each speaker model that setting needs for
// langs and isn't present yet (all of them if force).
func (m *Manager) DownloadSpeakerModels(setting string, langs []string, force bool, onProgress ProgressFunc) error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}
	status := m.Check()
	for _, sm := range SpeakerModelsNeeded(setting, langs) {
		if !force && status.SpeakerModelReady(sm) {
			continue
		}
		fmt.Printf("Downloading speaker model %s...\n", sm.Name)
		if err := downloadFile(sm.URL, status.SpeakerModelPath(sm), "Speaker model: "+sm.Name, onProgress); err != nil {
			return fmt.Errorf("downloading speaker model %s: %w", sm.Name, err)
		}
	}
	return nil
}

// SpeakerModelsNeeded lists the distinct models setting resolves to across
// langs.
func SpeakerModelsNeeded(setting string, langs []string) []SpeakerModel {
	var out []SpeakerModel
	seen := map[string]bool{}
	for _, l := range langs {
		sm := ResolveSpeakerModel(setting, l)
		if !seen[sm.ID] {
			seen[sm.ID] = true
			out = append(out, sm)
		}
	}
	return out
}
