package transcribe

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
)

// NewEngineSetFromConfig creates an EngineSet based on config and model status.
// If multilingual is enabled and models are available, returns an EngineSet with
// per-language engines. Otherwise returns an EngineSet with just the default
// (Parakeet) engine.
func NewEngineSetFromConfig(cfg Config, status *models.Status, multiCfg *config.MultilingualConfig) (*EngineSet, error) {
	defaultLang := "en"
	if multiCfg != nil && multiCfg.DefaultLang != "" {
		defaultLang = multiCfg.DefaultLang
	}

	// The model for the default language, as the setting picks it, if
	// downloaded (else the multilingual Parakeet).
	asr := models.ASRModels[0]
	if status != nil {
		var fellBack bool
		asr, fellBack = status.ASRModelFor(cfg.Model, defaultLang)
		if asr.Kind == models.ASRKindTransducer && asr.ID != models.ASRModels[0].ID {
			cfg.EncoderPath, cfg.DecoderPath, cfg.JoinerPath, cfg.TokensPath = status.ASRModelFiles(asr)
		}
		if fellBack {
			fmt.Printf("Transcription model %s isn't downloaded; using %s\n", models.ResolveASRModel(cfg.Model, defaultLang).Name, asr.Name)
		}
	}

	var parakeet Engine
	var err error
	if asr.Kind == models.ASRKindCohere {
		parakeet, err = NewCohereEngine(status.ASRModelDir(asr), defaultLang, cfg.NumThreads, cfg.VADPath)
	} else {
		parakeet, err = NewEngine(cfg)
	}
	if err != nil {
		return nil, err
	}

	engines := map[string]Engine{defaultLang: parakeet}

	// If multilingual enabled, create additional engines
	if multiCfg != nil && multiCfg.Enabled {
		for _, lang := range multiCfg.Languages {
			if lang == defaultLang {
				continue // already have Parakeet
			}
			if lang == "bn" && status.BengaliReady {
				bengali, err := NewBengaliEngine(BengaliConfig{
					EncoderPath: status.BengaliEncoderPath,
					DecoderPath: status.BengaliDecoderPath,
					JoinerPath:  status.BengaliJoinerPath,
					TokensPath:  status.BengaliTokensPath,
				})
				if err != nil {
					fmt.Printf("Warning: failed to create Bengali engine: %v\n", err)
					continue
				}
				engines["bn"] = bengali
				fmt.Println("Multilingual: Bengali Zipformer engine loaded")
			}
		}
	}

	es, err := NewEngineSet(engines, defaultLang)
	if err != nil {
		parakeet.Close()
		return nil, err
	}

	if len(engines) > 1 {
		fmt.Printf("Multilingual: %d engines available (default: %s)\n", len(engines), defaultLang)
	}

	return es, nil
}
