package backend

import (
	"fmt"
	"os"
	"reflect"
	"slices"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/hotkey"
)

// supportedLanguages are the session languages an engine exists for (see
// transcribe.NewEngineSetFromConfig).
var supportedLanguages = []string{"en", "bn"}

// ApplyResult tells the settings page what ApplySettings did.
type ApplyResult struct {
	// Applied lists what took effect in the running app, in plain words.
	Applied []string `json:"applied"`
	// Later lists what's saved but only used from the next recording or
	// dictation, because those settings are read when one starts.
	Later []string `json:"later"`
	// Warnings are things the user should act on, e.g. models missing
	// from a new model folder.
	Warnings []string `json:"warnings"`
}

// ApplySettings validates cfg, saves it to config.toml and applies it to the
// running app without a restart: speaker clustering tuning takes effect
// immediately, hotkeys are re-registered, and transcription engines, the
// meeting detector and the tray menu are rebuilt when their settings
// change. Devices and output settings are read when a recording or
// dictation starts, so they apply from the next one.
//
// Rebuilding the engines needs them idle, so a change that requires it is
// refused, with nothing saved, while a recording, dictation, save or
// re-transcription is using them.
func (a *App) ApplySettings(cfg config.Config) (*ApplyResult, error) {
	a.fixSignals()
	next := &cfg
	if err := validateSettings(next); err != nil {
		return nil, err
	}

	a.mu.Lock()
	if a.cfg == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("Tomoe is still starting up; try again in a moment")
	}
	plan := planApply(a.cfg, next)
	if plan.reloadEngines {
		if a.reconfiguring {
			a.mu.Unlock()
			return nil, errApplyingSettings
		}
		if busy := a.busyLocked(); busy != "" {
			a.mu.Unlock()
			return nil, fmt.Errorf("these changes reload the transcription engines, which can't happen while %s; try again when it's finished", busy)
		}
		// Blocks new recordings, dictation and re-transcription (see
		// leaseEnginesLocked) until the new engines are in place.
		a.reconfiguring = true
	}
	a.mu.Unlock()
	if plan.reloadEngines {
		defer func() {
			a.mu.Lock()
			a.reconfiguring = false
			a.mu.Unlock()
		}()
	}

	if err := config.Save(next, config.Path()); err != nil {
		return nil, fmt.Errorf("saving %s: %w", config.Path(), err)
	}

	result := &ApplyResult{}
	if plan.reloadEngines {
		result.Warnings = append(result.Warnings, a.swapEngines(next)...)
		result.Applied = append(result.Applied, "Transcription engines reloaded")
	}

	a.mu.Lock()
	a.cfg = next
	tracker := a.tracker
	oldDetector := a.detector
	a.mu.Unlock()

	if tracker != nil && plan.retune {
		tracker.SetTuning(tuningFromConfig(next))
		result.Applied = append(result.Applied, "Speaker clustering")
	}
	if plan.restartDetector {
		if oldDetector != nil {
			oldDetector.Stop()
		}
		detector := startDetector(a.ctx, next)
		a.mu.Lock()
		a.detector = detector
		a.mu.Unlock()
		if next.Meeting.AutoDetect && detector == nil {
			result.Warnings = append(result.Warnings, "Meeting auto-detect isn't available on this system")
		} else {
			result.Applied = append(result.Applied, "Meeting auto-detect")
		}
	}
	if plan.rebindHotkeys {
		if err := a.rebindHotkeys(); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Hotkeys: %v", err))
		} else {
			result.Applied = append(result.Applied, "Hotkeys")
		}
	}
	if plan.rebuildTray {
		a.rebuildTrayMenu()
		result.Applied = append(result.Applied, "Tray menu")
	}
	result.Later = plan.later
	return result, nil
}

// busyLocked names what's using the engines right now, or "" if nothing
// is. Caller must hold a.mu.
func (a *App) busyLocked() string {
	switch {
	case a.recording:
		return "a meeting is recording"
	case a.dictating:
		return "dictation is running"
	case a.engineLeases > 0:
		return "a session is still being saved or re-transcribed"
	}
	return ""
}

// applyPlan is what a settings change needs beyond saving it and swapping
// a.cfg (which is all that devices and output settings need).
type applyPlan struct {
	retune          bool
	reloadEngines   bool
	restartDetector bool
	rebindHotkeys   bool
	rebuildTray     bool
	// later lists changed settings that are only read when a recording or
	// dictation starts.
	later []string
}

// planApply works out what changing from old to next requires.
func planApply(old, next *config.Config) applyPlan {
	var p applyPlan
	p.retune = tuningFromConfig(old) != tuningFromConfig(next)
	ot, nt := old.Transcription, next.Transcription
	om, nm := old.Multilingual, next.Multilingual

	p.reloadEngines = !reflect.DeepEqual(ot, nt) ||
		om.Enabled != nm.Enabled || om.DefaultLang != nm.DefaultLang ||
		!slices.Equal(om.Languages, nm.Languages)
	p.restartDetector = old.Meeting.AutoDetect != next.Meeting.AutoDetect
	// The hotkey loops capture the default language and the detector's
	// event channel when they start.
	p.rebindHotkeys = old.Hotkey != next.Hotkey || om.DefaultLang != nm.DefaultLang || p.restartDetector
	p.rebuildTray = om.Enabled != nm.Enabled || om.DefaultLang != nm.DefaultLang ||
		!slices.Equal(om.Languages, nm.Languages)

	if old.Audio != next.Audio || old.Meeting.MonitorDevice != next.Meeting.MonitorDevice {
		p.later = append(p.later, "Audio devices")
	}
	if old.Output != next.Output {
		p.later = append(p.later, "Dictation output")
	}
	if old.Meeting.VideoHintPollInterval != next.Meeting.VideoHintPollInterval ||
		old.Meeting.VideoHintTriggerDebounce != next.Meeting.VideoHintTriggerDebounce {
		p.later = append(p.later, "Video hint timing")
	}
	return p
}

// validateSettings rejects values the app can't run with, naming the
// setting and what's wrong.
func validateSettings(c *config.Config) error {
	for name, b := range map[string]string{"Dictation hotkey": c.Hotkey.Binding, "Meeting hotkey": c.Hotkey.MeetingBinding} {
		if _, err := hotkey.ParseBinding(b); err != nil {
			return fmt.Errorf("%s %q: %w", name, b, err)
		}
	}
	if c.Hotkey.Binding == c.Hotkey.MeetingBinding {
		return fmt.Errorf("the dictation and meeting hotkeys can't be the same")
	}

	t := c.Transcription
	if t.ModelPath == "" {
		return fmt.Errorf("model folder can't be empty")
	}
	if t.DecodingMethod != "greedy_search" && t.DecodingMethod != "modified_beam_search" {
		return fmt.Errorf("decoding method must be greedy_search or modified_beam_search")
	}
	if t.MaxActivePaths < 1 {
		return fmt.Errorf("beam width must be at least 1")
	}
	if t.HotwordsScore < 0 {
		return fmt.Errorf("hotwords score can't be negative")
	}
	if t.HotwordsFile != "" {
		if _, err := os.Stat(t.HotwordsFile); err != nil {
			return fmt.Errorf("hotwords file %q: %w", t.HotwordsFile, err)
		}
		if t.DecodingMethod != "modified_beam_search" {
			return fmt.Errorf("hotwords only work with the modified_beam_search decoding method")
		}
	}

	if c.Output.SilenceTimeout < 0 {
		return fmt.Errorf("silence timeout can't be negative")
	}

	m := c.Multilingual
	if len(m.Languages) == 0 {
		return fmt.Errorf("choose at least one language")
	}
	for _, l := range m.Languages {
		if !slices.Contains(supportedLanguages, l) {
			return fmt.Errorf("unsupported language %q (supported: %v)", l, supportedLanguages)
		}
	}
	if !slices.Contains(m.Languages, m.DefaultLang) {
		return fmt.Errorf("the default language must be one of the chosen languages")
	}
	if m.DefaultLang != "en" {
		// The default language is always served by Parakeet (see
		// transcribe.NewEngineSetFromConfig), which doesn't do Bengali.
		return fmt.Errorf("the default language must be English for now; other languages are available per recording")
	}

	mt := c.Meeting
	if mt.SpeakerThreshold <= 0 || mt.SpeakerThreshold > 1 {
		return fmt.Errorf("speaker threshold must be above 0 and at most 1")
	}
	for name, v := range map[string]float64{
		"Sticky grace window":         mt.StickyGraceWindow,
		"Sticky threshold margin":     mt.StickyThresholdMargin,
		"Minimum assign duration":     mt.MinAssignDuration,
		"Short-segment grace window":  mt.ShortSegmentGraceWindow,
		"Video hint trigger debounce": mt.VideoHintTriggerDebounce,
	} {
		if v < 0 {
			return fmt.Errorf("%s can't be negative", name)
		}
	}
	if mt.VideoHintPollInterval <= 0 {
		return fmt.Errorf("video hint poll interval must be above 0")
	}
	return nil
}
