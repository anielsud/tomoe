// Package appinit is the first-run setup flow shared by the CLI and the
// GUI: detect the GPU, generate and save a default config.toml if one
// doesn't exist yet, and download any model that isn't already present.
// Both `tomoe`/`tomoe init` and tomoe-gui's Startup call EnsureInitialized
// so they behave identically — before this existed, the GUI silently
// skipped straight to "no engines" if models weren't downloaded, with no
// way to fix that short of running the CLI.
package appinit

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/gpu"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
)

// Result is what EnsureInitialized produced, for a caller to render its
// own summary from (the CLI prints text; the GUI moves on to building
// its transcription engines).
type Result struct {
	Config      *config.Config
	GPU         *gpu.Info
	ModelStatus *models.Status
}

// EnsureInitialized runs the shared init flow. Safe to call on every
// startup: config generation only happens once (a config.toml already
// on disk is loaded as-is, its GPU setting untouched; GPU detection
// still runs, for Result.GPU), and Manager.Download
// itself skips any model already present, so a fully-initialized system
// just re-verifies and returns quickly. onProgress (may be nil) is
// forwarded to Manager.Download for whichever models still need
// fetching.
func EnsureInitialized(onProgress models.ProgressFunc) (*Result, error) {
	var cfg *config.Config
	var gpuInfo *gpu.Info

	if config.Exists() {
		// A config.toml that fails to load is an error, not a cue to
		// fall back to defaults: silently running on defaults would
		// ignore the user's settings (model path included) with no hint
		// why.
		var err error
		cfg, err = config.Load(config.Path())
		if err != nil {
			return nil, fmt.Errorf("loading config %s: %w", config.Path(), err)
		}
	} else {
		gpuInfo = gpu.Detect()
		cfg = config.DefaultConfig()
		cfg.Transcription.GPUEnabled = gpuInfo.Available && gpuInfo.Sufficient
		if err := config.Save(cfg, config.Path()); err != nil {
			return nil, fmt.Errorf("saving config: %w", err)
		}
	}
	if gpuInfo == nil {
		gpuInfo = gpu.Detect()
	}

	mgr := models.NewManager(cfg.Transcription.ModelPath)
	if err := mgr.Download(false, onProgress); err != nil {
		return nil, fmt.Errorf("downloading models: %w", err)
	}
	// Not fatal: meetings use the base speaker model until it's there.
	if err := mgr.DownloadSpeakerModels(cfg.Meeting.SpeakerModel, cfg.MeetingLanguages(), false, onProgress); err != nil {
		fmt.Printf("Warning: %v (meetings will use the base speaker model)\n", err)
	}
	// Only two-pass uses the streaming model, and it's off by default.
	// Not fatal: sessions fall back to single-pass without it.
	if cfg.Transcription.TwoPass {
		if err := mgr.DownloadEnglishStreaming(false, onProgress); err != nil {
			fmt.Printf("Warning: %v (live transcription will use single-pass mode)\n", err)
		}
	}

	return &Result{Config: cfg, GPU: gpuInfo, ModelStatus: mgr.Check()}, nil
}
