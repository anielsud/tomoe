package backend

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
)

// Tool groups, in the order the Tools page shows them.
const (
	groupTools       = "Command-line tools"
	groupModels      = "Models"
	groupPermissions = "Permissions"
	groupGPU         = "GPU"
)

// ToolFix is how the Tools page offers to fix a missing dependency.
type ToolFix struct {
	// Kind is "action" (FixTool runs it, reporting progress as
	// tools:progress/tools:done events) or "command" (a terminal command
	// the page offers to copy, for fixes needing sudo or a rebuild).
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Command string `json:"command,omitempty"`
}

// ToolStatus is one dependency on the Tools page.
type ToolStatus struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
	OK    bool   `json:"ok"`
	// Required means Tomoe can't transcribe at all without it; everything
	// else turns off one feature (NeededFor).
	Required  bool     `json:"required"`
	NeededFor string   `json:"neededFor"`
	Detail    string   `json:"detail"` // where it was found, or what's wrong
	Fix       *ToolFix `json:"fix,omitempty"`
}

// ToolProgressEvent is "tools:progress"'s payload. Downloaded/Total are
// bytes for a model download, both 0 for a fix that only reports lines of
// output.
type ToolProgressEvent struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
}

// ToolDoneEvent is "tools:done"'s payload.
type ToolDoneEvent struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
	// Note is extra information for a fix that succeeded, e.g. that a
	// restart is needed.
	Note string `json:"note,omitempty"`
}

// GetTools reports every external dependency, model and (on macOS)
// permission Tomoe uses, with a fix for anything missing.
func (a *App) GetTools() []ToolStatus {
	a.fixSignals()
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	tools := []ToolStatus{ffmpegStatus(), diarizeWorkerStatus()}
	tools = append(tools, platformTools()...)
	tools = append(tools, modelStatuses(cfg)...)
	tools = append(tools, permissionStatuses()...)
	tools = append(tools, gpuStatuses(cfg)...)
	return tools
}

// fixRunning tracks fixes in progress, so a double click can't start the
// same download or install twice.
var (
	fixMu      sync.Mutex
	fixRunning = map[string]bool{}
)

// FixTool starts the "action" fix for a tool (see GetTools) in the
// background and returns immediately. Progress arrives as tools:progress
// events and the outcome as a tools:done event.
func (a *App) FixTool(id string) error {
	a.fixSignals()
	fix, err := a.fixFor(id)
	if err != nil {
		return err
	}

	fixMu.Lock()
	if fixRunning[id] {
		fixMu.Unlock()
		return fmt.Errorf("already running")
	}
	fixRunning[id] = true
	fixMu.Unlock()

	go func() {
		note, err := fix()
		fixMu.Lock()
		delete(fixRunning, id)
		fixMu.Unlock()
		done := ToolDoneEvent{ID: id, Note: note}
		if err != nil {
			done.Error = err.Error()
		}
		wailsRuntime.EventsEmit(a.ctx, "tools:done", done)
	}()
	return nil
}

// CopyToClipboard puts text on the clipboard, for the Tools page's
// "copy command" fixes.
func (a *App) CopyToClipboard(text string) error {
	a.fixSignals()
	return wailsRuntime.ClipboardSetText(a.ctx, text)
}

// fixFor returns the function that runs id's fix, returning an optional
// note for the user.
func (a *App) fixFor(id string) (func() (string, error), error) {
	if id == "models" || strings.HasPrefix(id, "model-") {
		return func() (string, error) { return a.downloadModels(id) }, nil
	}
	if f := platformFix(a, id); f != nil {
		return f, nil
	}
	return nil, fmt.Errorf("no automatic fix for %q", id)
}

// downloadModels downloads every missing model (the same set `tomoe init`
// fetches, plus Bengali if it's a chosen language), then reloads the
// engines so the new models are used without a restart.
func (a *App) downloadModels(id string) (string, error) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	if cfg == nil {
		return "", fmt.Errorf("Tomoe is still starting up")
	}
	progress := func(step string, downloaded, total int64) {
		wailsRuntime.EventsEmit(a.ctx, "tools:progress", ToolProgressEvent{ID: id, Message: step, Downloaded: downloaded, Total: total})
	}
	mgr := models.NewManager(cfg.Transcription.ModelPath).WithLiveModel(cfg.Transcription.LiveModel)
	if err := mgr.Download(false, progress); err != nil {
		return "", err
	}
	if err := mgr.DownloadSpeakerModels(cfg.Meeting.SpeakerModel, cfg.MeetingLanguages(), false, progress); err != nil {
		return "", err
	}
	if err := mgr.DownloadASRModels(cfg.Transcription.Model, cfg.MeetingLanguages(), false, progress); err != nil {
		return "", err
	}
	if cfg.Meeting.VideoHintsOn() {
		if err := mgr.DownloadOCRModels(false, progress); err != nil {
			return "", err
		}
	}
	if cfg.Transcription.TwoPass {
		if err := mgr.DownloadEnglishStreaming(false, progress); err != nil {
			return "", err
		}
	}
	if wantsBengali(cfg) {
		if err := mgr.DownloadMultilingual(false, progress); err != nil {
			return "", err
		}
	}

	warnings, err := a.reloadEngines(cfg)
	if err != nil {
		return fmt.Sprintf("Downloaded. %v; the new models load the next time the engines reload (apply settings or restart Tomoe).", err), nil
	}
	return strings.Join(warnings, " "), nil
}

// reloadEngines rebuilds the engine bundle for cfg and swaps it in, once
// nothing is decoding with the current one. Returns warnings about models
// that are still missing.
func (a *App) reloadEngines(cfg *config.Config) ([]string, error) {
	a.mu.Lock()
	if a.reconfiguring {
		a.mu.Unlock()
		return nil, errApplyingSettings
	}
	if busy := a.busyLocked(); busy != "" {
		a.mu.Unlock()
		return nil, fmt.Errorf("the engines can't reload while %s", busy)
	}
	a.reconfiguring = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.reconfiguring = false
		a.mu.Unlock()
	}()
	return a.swapEngines(cfg), nil
}

// swapEngines builds and installs a new bundle and closes the old one.
// Caller must have set a.reconfiguring, with nothing leasing the engines.
func (a *App) swapEngines(cfg *config.Config) []string {
	status := models.NewManager(cfg.Transcription.ModelPath).WithLiveModel(cfg.Transcription.LiveModel).Check()
	bundle := buildEngines(cfg, status)
	a.mu.Lock()
	old := a.bundle
	a.bundle = bundle
	a.mu.Unlock()
	old.close()

	var warnings []string
	if bundle.engines == nil {
		warnings = append(warnings, fmt.Sprintf("No transcription models in %s: open Tools to download them.", cfg.Transcription.ModelPath))
	}
	for _, sm := range models.SpeakerModelsNeeded(cfg.Meeting.SpeakerModel, cfg.MeetingLanguages()) {
		if !status.SpeakerModelReady(sm) {
			warnings = append(warnings, fmt.Sprintf("The speaker model %s isn't downloaded, so meetings use the base model until it is: open Tools to download it.", sm.Name))
		}
	}
	for _, am := range models.ASRModelsNeeded(cfg.Transcription.Model, cfg.MeetingLanguages()) {
		if !status.ASRModelReady(am) {
			warnings = append(warnings, fmt.Sprintf("The transcription model %s isn't downloaded, so transcription uses %s until it is: open Tools to download it.", am.Name, models.ASRModels[0].Name))
		}
	}
	if cfg.Transcription.TwoPass && bundle.streamingEngine == nil {
		warnings = append(warnings, "Two-pass is on, but the English streaming model isn't available: open Tools to download it.")
	}
	if cfg.Meeting.VideoHintsOn() && !status.OCRReady() {
		warnings = append(warnings, "Video hints can't read names until the text-reading models are downloaded: open Tools to download them.")
	}
	return warnings
}

// speakerModelUse names the meeting languages cfg uses sm for, e.g.
// "English".
func speakerModelUse(cfg *config.Config, sm models.SpeakerModel) string {
	var names []string
	for _, l := range cfg.MeetingLanguages() {
		if models.ResolveSpeakerModel(cfg.Meeting.SpeakerModel, l).ID == sm.ID {
			names = append(names, languageName(l))
		}
	}
	return strings.Join(names, " and ")
}

func wantsBengali(cfg *config.Config) bool {
	return cfg.Multilingual.Enabled && slices.Contains(cfg.Multilingual.Languages, "bn")
}

// runStreaming runs cmd, reporting each line of its output as a
// tools:progress message for id.
func (a *App) runStreaming(id string, cmd *exec.Cmd) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			wailsRuntime.EventsEmit(a.ctx, "tools:progress", ToolProgressEvent{ID: id, Message: sc.Text()})
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	return err
}

func ffmpegStatus() ToolStatus {
	t := ToolStatus{
		ID: "ffmpeg", Name: "ffmpeg", Group: groupTools,
		NeededFor: "Saving meeting audio, re-transcribing and replaying sessions",
	}
	if path, err := toolpath.FFmpeg(); err == nil {
		t.OK, t.Detail = true, path
	} else {
		t.Detail = "Not found: meetings are saved without audio"
		t.Fix = ffmpegFix()
	}
	return t
}

func diarizeWorkerStatus() ToolStatus {
	t := ToolStatus{
		ID: "diarize-worker", Name: "tomoe CLI (diarization worker)", Group: groupTools,
		NeededFor: "Relabeling speakers after a meeting is saved",
	}
	if path := session.FindDiarizeWorker(); path != "" {
		t.OK, t.Detail = true, path
	} else {
		t.Detail = "Not found next to Tomoe or on PATH: speakers aren't relabeled after saving"
		t.Fix = diarizeWorkerFix()
	}
	return t
}

// modelStatuses lists each model with what it's needed for. They all
// share one "download missing models" fix, since that's how the model
// manager downloads.
func modelStatuses(cfg *config.Config) []ToolStatus {
	s := models.NewManager(cfg.Transcription.ModelPath).WithLiveModel(cfg.Transcription.LiveModel).Check()
	fix := &ToolFix{Kind: "action", Label: "Download missing models"}
	entry := func(id, name, neededFor string, required, ok bool) ToolStatus {
		t := ToolStatus{ID: id, Name: name, Group: groupModels, Required: required, NeededFor: neededFor, OK: ok}
		if ok {
			t.Detail = "Downloaded"
		} else {
			t.Detail = "Not downloaded"
			t.Fix = fix
		}
		return t
	}
	parakeet := entry("model-parakeet", "Parakeet TDT (speech recognition)", "All transcription except Bengali", true, s.ParakeetReady)
	if s.ParakeetPartial {
		parakeet.Detail = "Incomplete download"
	}
	streaming := entry("model-streaming", "Live model: "+s.EnglishStreamingName, "Live text while people speak (two-pass)", false, s.EnglishStreamingReady)
	if !cfg.Transcription.TwoPass {
		// Only two-pass uses it, and that's off: not missing, just unused.
		streaming.OK, streaming.Fix = true, nil
		if s.EnglishStreamingReady {
			streaming.Detail = "Downloaded (two-pass is off in Settings)"
		} else {
			streaming.Detail = "Not needed while two-pass is off (turning it on in Settings will ask for it)"
		}
	}
	list := []ToolStatus{
		parakeet,
		entry("model-vad", "Silero VAD", "Detecting when someone is speaking", true, s.VADReady),
		streaming,
		entry("model-speaker", "Speaker embedding: "+models.SpeakerModels[0].Name, "Telling remote speakers apart in meetings", false, s.SpeakerEmbeddingReady),
	}
	for _, sm := range models.SpeakerModelsNeeded(cfg.Meeting.SpeakerModel, cfg.MeetingLanguages()) {
		if sm.ID != models.SpeakerModels[0].ID {
			list = append(list, entry("model-speaker-"+sm.ID, "Speaker embedding: "+sm.Name, "Telling speakers apart in "+speakerModelUse(cfg, sm)+" meetings (Settings, Speaker model)", false, s.SpeakerModelReady(sm)))
		}
	}
	for _, am := range models.ASRModelsNeeded(cfg.Transcription.Model, cfg.MeetingLanguages()) {
		list = append(list, entry("model-asr-"+am.ID, "Transcription: "+am.Name, "More accurate transcription (config: transcription.model)", false, s.ASRModelReady(am)))
	}
	list = append(list,
		entry("model-segmentation", "Speaker segmentation", "Relabeling speakers after a meeting is saved", false, s.SpeakerSegmentationReady),
	)
	if wantsBengali(cfg) {
		list = append(list, entry("model-bengali", "Bengali Zipformer", "Bengali transcription", false, s.BengaliReady))
	}
	if cfg.Meeting.VideoHintsOn() {
		list = append(list, entry("model-ocr", "Text reading (PP-OCRv5)", "Reading names off the meeting window (video hints)", false, s.OCRReady()))
	}
	for i := range list {
		list[i].Detail += " · " + s.ModelDir
	}
	return list
}

// commandFix is a fix the user runs in a terminal.
func commandFix(label, command string) *ToolFix {
	return &ToolFix{Kind: "command", Label: label, Command: command}
}

// languageName is lang's English name, for messages.
func languageName(lang string) string {
	switch lang {
	case "en":
		return "English"
	case "bn":
		return "Bengali"
	}
	return lang
}
