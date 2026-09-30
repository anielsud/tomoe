package backend

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

func TestPlanApply_NoChangeNeedsNothing(t *testing.T) {
	p := planApply(config.DefaultConfig(), config.DefaultConfig())
	if p.retune || p.reloadEngines || p.restartDetector || p.rebindHotkeys || p.rebuildTray || len(p.later) != 0 {
		t.Errorf("unchanged config planned %+v", p)
	}
}

func TestPlanApply(t *testing.T) {
	cases := []struct {
		name   string
		change func(*config.Config)
		check  func(applyPlan) bool
	}{
		{"clustering tuning only retunes", func(c *config.Config) { c.Meeting.SpeakerThreshold = 0.65 },
			func(p applyPlan) bool { return p.retune && !p.reloadEngines && !p.rebindHotkeys }},
		{"two-pass reloads engines", func(c *config.Config) { c.Transcription.TwoPass = false },
			func(p applyPlan) bool { return p.reloadEngines && !p.rebuildTray }},
		{"GPU reloads engines", func(c *config.Config) { c.Transcription.GPUEnabled = true },
			func(p applyPlan) bool { return p.reloadEngines }},
		{"languages reload engines and rebuild tray", func(c *config.Config) {
			c.Multilingual.Enabled = true
			c.Multilingual.Languages = []string{"en", "bn"}
		}, func(p applyPlan) bool { return p.reloadEngines && p.rebuildTray && !p.rebindHotkeys }},
		{"hotkey rebinds only", func(c *config.Config) { c.Hotkey.Binding = "Ctrl+Shift+D" },
			func(p applyPlan) bool { return p.rebindHotkeys && !p.reloadEngines }},
		{"auto-detect restarts detector and rebinds", func(c *config.Config) { c.Meeting.AutoDetect = false },
			func(p applyPlan) bool { return p.restartDetector && p.rebindHotkeys && !p.reloadEngines }},
		{"devices apply later", func(c *config.Config) { c.Audio.Device = "USB Mic" },
			func(p applyPlan) bool { return slices.Contains(p.later, "Audio devices") && !p.reloadEngines }},
		{"output applies later", func(c *config.Config) { c.Output.AutoPaste = false },
			func(p applyPlan) bool { return slices.Contains(p.later, "Dictation output") }},
	}
	for _, c := range cases {
		next := config.DefaultConfig()
		c.change(next)
		if p := planApply(config.DefaultConfig(), next); !c.check(p) {
			t.Errorf("%s: got %+v", c.name, p)
		}
	}
}

func TestValidateSettings_DefaultIsValid(t *testing.T) {
	if err := validateSettings(config.DefaultConfig()); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
}

func TestValidateSettings_Rejects(t *testing.T) {
	cases := []struct {
		change func(*config.Config)
		want   string
	}{
		{func(c *config.Config) { c.Hotkey.Binding = "Nope+Q" }, "Dictation hotkey"},
		{func(c *config.Config) { c.Hotkey.Binding = c.Hotkey.MeetingBinding }, "same"},
		{func(c *config.Config) { c.Transcription.DecodingMethod = "fast" }, "decoding method"},
		{func(c *config.Config) { c.Transcription.MaxActivePaths = 0 }, "beam width"},
		{func(c *config.Config) { c.Transcription.HotwordsFile = "/no/such/hotwords.txt" }, "hotwords file"},
		{func(c *config.Config) { c.Multilingual.Languages = nil }, "at least one language"},
		{func(c *config.Config) { c.Multilingual.Languages = []string{"en", "fr"} }, "unsupported language"},
		{func(c *config.Config) {
			c.Multilingual.Languages = []string{"en", "bn"}
			c.Multilingual.DefaultLang = "bn"
		}, "must be English"},
		{func(c *config.Config) { c.Meeting.SpeakerThreshold = 0 }, "speaker threshold"},
		{func(c *config.Config) { c.Meeting.SpeakerThreshold = 1.5 }, "speaker threshold"},
		{func(c *config.Config) { c.Meeting.MinAssignDuration = -1 }, "Minimum assign duration"},
		{func(c *config.Config) { c.Meeting.VideoHintPollInterval = 0 }, "poll interval"},
		{func(c *config.Config) { c.Output.SilenceTimeout = -1 }, "silence timeout"},
	}
	for _, c := range cases {
		cfg := config.DefaultConfig()
		c.change(cfg)
		err := validateSettings(cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want error containing %q, got %v", c.want, err)
		}
	}
}

func TestValidateSettings_HotwordsWithGreedyAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hotwords.txt")
	if err := os.WriteFile(path, []byte("Tomoe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An existing config may pair hotwords with greedy search (sherpa-onnx
	// just doesn't use them); that must not block applying other changes.
	cfg := config.DefaultConfig()
	cfg.Transcription.HotwordsFile = path
	if err := validateSettings(cfg); err != nil {
		t.Errorf("greedy_search with hotwords rejected: %v", err)
	}
}

func TestLeaseEngines_BlockedWhileReconfiguring(t *testing.T) {
	a := &App{}
	a.bundle.engines = nil
	if _, _, err := a.leaseEnginesLocked(); err == nil {
		t.Error("lease granted without engines")
	}
	a.reconfiguring = true
	if _, _, err := a.leaseEnginesLocked(); err != errApplyingSettings {
		t.Errorf("lease while reconfiguring: got %v, want errApplyingSettings", err)
	}
}
