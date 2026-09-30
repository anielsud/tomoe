package main

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// offlinePipeline holds the models an offline run of the live pipeline
// needs (`tomoe session replay`, `tomoe eval`), loaded once and shared by
// every run.
type offlinePipeline struct {
	status  *models.Status
	engines *transcribe.EngineSet
	engine  transcribe.Engine
	// embedder is nil when speaker clustering wasn't requested or its
	// model isn't downloaded; streaming is nil unless requested and
	// downloaded.
	embedder  *speaker.Embedder
	streaming transcribe.StreamingEngine
}

// loadOfflinePipeline loads the transcription engine for lang, plus the
// speaker embedder and the English streaming engine when asked for.
// Missing optional models are reported and skipped, not fatal.
func loadOfflinePipeline(cfg *config.Config, status *models.Status, lang string, withEmbedder, withStreaming bool) (*offlinePipeline, error) {
	return loadOfflinePipelineThreads(cfg, status, lang, withEmbedder, withStreaming, 0)
}

// loadOfflinePipelineThreads is loadOfflinePipeline with a CPU thread count
// for the transcription engine (0 = the engine's default).
func loadOfflinePipelineThreads(cfg *config.Config, status *models.Status, lang string, withEmbedder, withStreaming bool, threads int) (*offlinePipeline, error) {
	p := &offlinePipeline{status: status}
	engines, err := transcribe.NewEngineSetFromConfig(transcribe.Config{
		EncoderPath:    status.EncoderPath,
		DecoderPath:    status.DecoderPath,
		JoinerPath:     status.JoinerPath,
		TokensPath:     status.TokensPath,
		VADPath:        status.VADPath,
		UseGPU:         cfg.Transcription.GPUEnabled,
		DecodingMethod: cfg.Transcription.DecodingMethod,
		MaxActivePaths: cfg.Transcription.MaxActivePaths,
		HotwordsFile:   cfg.Transcription.HotwordsFile,
		HotwordsScore:  cfg.Transcription.HotwordsScore,
		NumThreads:     threads,
	}, status, &cfg.Multilingual)
	if err != nil {
		return nil, fmt.Errorf("creating transcription engine: %w", err)
	}
	p.engines = engines
	if p.engine = engines.Get(lang); p.engine == nil {
		p.Close()
		return nil, fmt.Errorf("no transcription engine for language %q", lang)
	}

	if withEmbedder && status.SpeakerEmbeddingReady {
		if p.embedder, err = speaker.NewEmbedder(status.SpeakerEmbeddingPath); err != nil {
			p.Close()
			return nil, fmt.Errorf("loading speaker embedding model: %w", err)
		}
	}
	if withStreaming && lang == "en" {
		if !status.EnglishStreamingReady {
			fmt.Println("Note: English streaming model not downloaded; two-pass runs are single-pass (tomoe model download --streaming).")
		} else if p.streaming, err = transcribe.NewStreamingEngine(transcribe.StreamingConfig{
			EncoderPath: status.EnglishStreamingEncoderPath,
			DecoderPath: status.EnglishStreamingDecoderPath,
			JoinerPath:  status.EnglishStreamingJoinerPath,
			TokensPath:  status.EnglishStreamingTokensPath,
		}); err != nil {
			p.Close()
			return nil, fmt.Errorf("loading English streaming model: %w", err)
		}
	}
	return p, nil
}

// liveConfig builds the live.Config for one run: a fresh tracker with
// tuning, and two-pass only when asked for and the streaming engine loaded.
func (p *offlinePipeline) liveConfig(tuning speaker.Tuning, twoPass bool) live.Config {
	lc := live.Config{Engine: p.engine, VADPath: p.status.VADPath}
	if p.embedder != nil {
		tracker := speaker.NewTracker(tuning.Threshold)
		tracker.SetTuning(tuning)
		lc.Embedder, lc.Tracker = p.embedder, tracker
	}
	if twoPass {
		lc.StreamingEngine = p.streaming
	}
	return lc
}

// Close releases every loaded model.
func (p *offlinePipeline) Close() {
	if p.streaming != nil {
		p.streaming.Close()
	}
	if p.embedder != nil {
		p.embedder.Close()
	}
	if p.engines != nil {
		p.engines.Close()
	}
}
