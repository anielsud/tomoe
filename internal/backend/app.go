package backend

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/appinit"
	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/gpu"
	"github.com/sosuke-ai/tomoe-pc/internal/hotkey"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/meetingaudio"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// App is the Wails backend, bound to the frontend via bindings.
type App struct {
	ctx context.Context
	cfg *config.Config

	// bundle holds the transcription/streaming engines, speaker
	// embedder and model manager (see engineBundle). Guarded by mu:
	// reloadEngines swaps it when settings change.
	bundle      engineBundle
	tracker     *speaker.Tracker
	coordinator *live.Coordinator
	store       *session.Store
	detector    *meeting.Detector
	// unregisterHotkeys stops the hotkey listen loops and releases their
	// bindings (see registerHotkeys). Guarded by mu.
	unregisterHotkeys func()

	// engineLeases counts decoding jobs using the current bundle (see
	// leaseEnginesLocked), and reconfiguring is set while ApplySettings
	// swaps it; together they keep a bundle from being closed while
	// anything still decodes with it. Both guarded by mu.
	engineLeases  int
	reconfiguring bool
	// sessionRelease ends the current recording's engine lease; it moves
	// to the session's saveRequest on stop, since pass-2 refinement keeps
	// decoding until the save has its last segment.
	sessionRelease func()
	// dictRelease ends the current dictation's engine lease.
	dictRelease func()

	mu              sync.Mutex
	recording       bool // meeting recording in progress
	dictating       bool // dictation recording in progress
	dictCoordinator *live.Coordinator
	dictCancel      context.CancelFunc
	currentSess     *session.Session
	videoHintCancel context.CancelFunc
	// segmentsDone is closed once the current session's coordinator has
	// delivered its last segment and refinement (see emitSessionSegments).
	segmentsDone <-chan struct{}

	// configWatchStop stops the config.toml hot-reload watcher started
	// in Startup (see speaker.Tracker.SetTuning) once the app shuts
	// down. nil if no tracker/embedder was created.
	configWatchStop func()

	// videoHintMu guards videoHintActivity, a short ring buffer of the
	// current session's videohint.Event trace — separate from mu since
	// emitVideoHintEvents runs concurrently with the rest of the session
	// lifecycle and has no reason to contend with it.
	videoHintMu       sync.Mutex
	videoHintActivity []videohint.Event

	// trayDictCh is signalled by the tray "Start/Stop Dictation" menu item.
	// Carries language code; "" = stop.
	trayDictCh chan string
	// trayMeetCh is signalled by the tray "Start/Stop Meeting" menu item.
	// Carries language code; "" = stop.
	trayMeetCh chan string
	tray       *trayManager

	// Background save pipeline. Each StopSession enqueues; one worker
	// drains serially so concurrent diarization can't corrupt sherpa-onnx
	// state. Buffer keeps the foreground non-blocking under burst.
	saveQueue chan *saveRequest
	saveWG    sync.WaitGroup

	// shuttingDown (guarded by mu) refuses new sessions once Shutdown has
	// begun; stopWG counts StopSession calls between claiming a session
	// and enqueueing its save, which Shutdown waits for before closing
	// saveQueue.
	shuttingDown bool
	stopWG       sync.WaitGroup

	// initDone/initErr (guarded by mu) record runInit's outcome for
	// InitStatus -- the frontend's fallback for the case where runInit
	// finishes (the common case resolves in milliseconds: everything's
	// already downloaded) before it's even mounted and subscribed to
	// the one-shot init:done/init:failed events, which it would
	// otherwise miss entirely and stay stuck on the init screen forever.
	initDone bool
	initErr  string
}

// saveRequest is a unit of work for the background save worker.
type saveRequest struct {
	sess         *session.Session
	coordinator  *live.Coordinator
	segmentsDone <-chan struct{} // see App.segmentsDone
	release      func()          // ends the session's engine lease; see App.sessionRelease
}

const saveQueueDepth = 16

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{
		trayDictCh: make(chan string, 1),
		trayMeetCh: make(chan string, 1),
		saveQueue:  make(chan *saveRequest, saveQueueDepth),
	}
}

// InitProgressEvent is "init:progress"'s payload: download progress for
// one step of first-run setup (see appinit.EnsureInitialized). Total is
// 0 if not yet known (the response hasn't arrived) or the step doesn't
// involve a download at all.
type InitProgressEvent struct {
	Step       string `json:"step"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
}

// Startup is called by Wails when the application starts. Runs first-run
// setup (see runInit) in the background so the window appears
// immediately: the frontend shows an init screen driven by
// init:progress/init:done/init:failed events until it completes, rather
// than this call blocking Wails' own startup on what can be a ~690MB
// download.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	// Start the background save worker. Drains pending saves on Shutdown
	// so we never lose a session that was queued before app exit.
	a.saveWG.Add(1)
	go a.saveWorker()

	// Has no heavy/network dependencies, so it's ready immediately
	// rather than waiting on runInit.
	a.store = session.NewStore(config.SessionDir())

	go a.runInit(ctx)
}

// runInit runs the same first-run setup flow the CLI's `tomoe`/`tomoe
// init` does (see appinit.EnsureInitialized): generate config.toml if
// one doesn't exist yet, then download any model that isn't already
// present. Previously this package silently skipped straight to "no
// engines" if models weren't downloaded, with no way to fix that short
// of running the CLI -- this makes the GUI self-sufficient.
func (a *App) runInit(ctx context.Context) {
	result, err := appinit.EnsureInitialized(func(step string, downloaded, total int64) {
		wailsRuntime.EventsEmit(a.ctx, "init:progress", InitProgressEvent{Step: step, Downloaded: downloaded, Total: total})
	})
	if err != nil {
		a.mu.Lock()
		a.initErr = err.Error()
		a.mu.Unlock()
		wailsRuntime.EventsEmit(a.ctx, "init:failed", err.Error())
		return
	}

	a.mu.Lock()
	a.cfg = result.Config
	a.mu.Unlock()
	a.setupEngines(ctx, result.Config, result.ModelStatus)

	a.mu.Lock()
	a.initDone = true
	a.mu.Unlock()
	wailsRuntime.EventsEmit(a.ctx, "init:done", nil)
}

// InitStatusView is InitStatus's return value.
type InitStatusView struct {
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// InitStatus reports runInit's current outcome. The frontend calls this
// right after subscribing to init:progress/init:done/init:failed, in
// case runInit already finished (the common case: everything already
// downloaded, so it resolves in milliseconds) before the frontend even
// mounted -- those events fire once and don't replay, so a late
// subscriber would otherwise never learn it's done and stay stuck on
// the init screen forever.
func (a *App) InitStatus() InitStatusView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return InitStatusView{Done: a.initDone, Error: a.initErr}
}

// setupEngines builds everything that depends on cfg/status: the
// transcription engine(s), the optional realtime streaming and speaker
// embedding/clustering pipelines, the meeting auto-detector, tray and
// hotkeys. Split out of Startup so it can run once runInit's
// (network-dependent) setup has actually completed.
//
// Everything is built into local variables first and only assigned onto
// the App struct under a.mu right before StartTrayAsync/registerHotkeys
// -- StartSession and others read a.bundle and a.tracker under that
// same lock, and runInit now runs this from its own goroutine rather
// than Startup's, so (unlike when this all ran inline
// in Startup, before anything else could call in) these writes are no
// longer implicitly ordered before a bound method could observe them.
func (a *App) setupEngines(ctx context.Context, cfg *config.Config, status *models.Status) {
	bundle := buildEngines(cfg, status)

	// The tracker doesn't depend on any model, so it's created even
	// without the speaker embedding model (Assign is simply never called
	// then) and survives engine reloads with its tuning intact.
	tracker := speaker.NewTracker(speaker.DefaultThreshold)
	tracker.SetTuning(tuningFromConfig(cfg))

	// Watch config.toml so clustering tuning can be retuned live -- no
	// rebuild, no relaunch. See MeetingConfig's doc comment for why this
	// exists. tracker has its own internal lock (see speaker.Tracker), so
	// this closure needs no synchronization against a.mu of its own.
	configWatchStop := config.Watch(config.Path(), 2*time.Second, func(newCfg *config.Config) {
		tracker.SetTuning(tuningFromConfig(newCfg))
		fmt.Printf("config: reloaded speaker clustering tuning: %+v\n", tracker.Tuning())
	})

	detector := startDetector(ctx, cfg)

	a.mu.Lock()
	a.bundle = bundle
	a.tracker = tracker
	a.configWatchStop = configWatchStop
	a.detector = detector
	a.mu.Unlock()

	// Start system tray (after engines are loaded so language menus are correct)
	StartTrayAsync(a)

	// Register meeting hotkey
	if err := a.registerHotkeys(); err != nil {
		// Non-fatal — hotkey may not be available in all environments
		fmt.Printf("Warning: could not register meeting hotkey: %v\n", err)
	}
}

// engineBundle is everything built from the config and the downloaded
// models that sessions decode with. Rebuilt as a unit when a settings
// change needs it (see reloadEngines).
type engineBundle struct {
	modelMgr *models.Manager
	engines  *transcribe.EngineSet
	// streamingEngine powers live transcription's realtime ("pass 1")
	// pass; nil if the English streaming model isn't downloaded or
	// two_pass is off, in which case sessions fall back to single-pass.
	// English-only today, so only wired into live.Config when the
	// selected session language is "en" — see StartSession.
	streamingEngine transcribe.StreamingEngine
	// embedders holds the speaker models meetings use, one per model
	// (see models.ResolveSpeakerModel); nil if none is downloaded.
	embedders *speaker.EmbedderSet
}

// buildEngines builds an engineBundle for cfg. Anything whose models
// aren't downloaded (or fail to load) is left nil; callers already treat
// each piece as optional except engines, which StartSession requires.
func buildEngines(cfg *config.Config, status *models.Status) engineBundle {
	b := engineBundle{modelMgr: models.NewManager(cfg.Transcription.ModelPath)}

	// Create transcription engine if models are ready
	if status.Ready() {
		built, err := transcribe.NewEngineSetFromConfig(transcribe.Config{
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
		}, status, &cfg.Multilingual)
		if err == nil {
			b.engines = built
		} else {
			fmt.Printf("Warning: failed to create transcription engine: %v\n", err)
		}
	}

	// Create the realtime streaming engine if the English streaming
	// model is available (see internal/live's two-pass pipeline). Not
	// required for anything else to work — sessions just fall back to
	// single-pass without it.
	if status.EnglishStreamingReady && cfg.Transcription.TwoPass {
		built, err := transcribe.NewStreamingEngine(transcribe.StreamingConfig{
			EncoderPath: status.EnglishStreamingEncoderPath,
			DecoderPath: status.EnglishStreamingDecoderPath,
			JoinerPath:  status.EnglishStreamingJoinerPath,
			TokensPath:  status.EnglishStreamingTokensPath,
		})
		if err == nil {
			b.streamingEngine = built
		} else {
			fmt.Printf("Warning: failed to load English streaming model: %v (live transcription will use single-pass mode)\n", err)
		}
	}

	// Load the speaker model each meeting language uses, if downloaded.
	set := speaker.NewEmbedderSet()
	loaded := false
	for _, lang := range cfg.MeetingLanguages() {
		if m, path, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang); status.SpeakerModelReady(m) {
			if _, err := set.Get(path); err == nil {
				loaded = true
			} else {
				fmt.Printf("Warning: failed to load speaker embedding model: %v\n", err)
			}
		}
	}
	if loaded {
		b.embedders = set
	}
	return b
}

// close releases everything in the bundle. Only safe once nothing can
// still be decoding with it (see reloadEngines and Shutdown).
func (b engineBundle) close() {
	if b.engines != nil {
		b.engines.Close()
	}
	if b.streamingEngine != nil {
		b.streamingEngine.Close()
	}
	if b.embedders != nil {
		b.embedders.Close()
	}
}

// meetingEmbedder is the speaker embedder for a meeting in lang (see
// models.Status.SpeakerModelFor), or nil if no speaker model is available.
func meetingEmbedder(cfg *config.Config, status *models.Status, set *speaker.EmbedderSet, lang string) *speaker.Embedder {
	m, path, fellBack := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang)
	if set == nil || !status.SpeakerModelReady(m) {
		return nil
	}
	if fellBack {
		fmt.Printf("Speaker model %s isn't downloaded; using %s\n", models.ResolveSpeakerModel(cfg.Meeting.SpeakerModel, lang).Name, m.Name)
	}
	e, err := set.Get(path)
	if err != nil {
		fmt.Printf("Warning: failed to load speaker model %s: %v\n", m.Name, err)
		return nil
	}
	return e
}

// tuningFromConfig is the speaker clustering tuning cfg asks for.
func tuningFromConfig(cfg *config.Config) speaker.Tuning {
	return speaker.TuningFromSeconds(
		cfg.Meeting.SpeakerThreshold,
		cfg.Meeting.StickyGraceWindow,
		cfg.Meeting.StickyThresholdMargin,
		cfg.Meeting.MinAssignDuration,
		cfg.Meeting.ShortSegmentGraceWindow,
	)
}

// startDetector starts the meeting auto-detector if cfg enables it, or
// returns nil (also when it's unavailable on this system).
func startDetector(ctx context.Context, cfg *config.Config) *meeting.Detector {
	if !cfg.Meeting.AutoDetect {
		return nil
	}
	d := meeting.NewDetector()
	if err := d.Start(ctx); err != nil {
		fmt.Printf("Warning: meeting auto-detect unavailable: %v\n", err)
		return nil
	}
	return d
}

// Shutdown is called by Wails when the application is closing.
func (a *App) Shutdown(ctx context.Context) {
	StopTray()

	if a.configWatchStop != nil {
		a.configWatchStop()
	}

	// Snapshot mutable fields under lock before acting on them.
	a.mu.Lock()
	a.shuttingDown = true
	recording := a.recording
	dictCoord := a.dictCoordinator
	dictCancel := a.dictCancel
	a.mu.Unlock()

	if recording {
		_, _ = a.StopSession()
	}
	if dictCoord != nil {
		dictCoord.Stop()
	}
	if dictCancel != nil {
		dictCancel()
	}
	if a.detector != nil {
		a.detector.Stop()
	}

	// A StopSession from the tray, hotkey or meeting detector that claimed
	// the session just before Shutdown may still be about to enqueue its
	// save; closing saveQueue under it would panic (send on closed
	// channel) and lose the session.
	a.stopWG.Wait()

	// Drain pending saves before closing engines/embedder, since a save in
	// flight may still be using sherpa-onnx state.
	if a.saveQueue != nil {
		close(a.saveQueue)
		a.saveWG.Wait()
		a.saveQueue = nil
	}

	a.mu.Lock()
	bundle := a.bundle
	a.bundle = engineBundle{}
	a.mu.Unlock()
	bundle.close()
}

// BeforeClose is called before the window closes. Returns true to prevent closing.
func (a *App) BeforeClose(ctx context.Context) bool {
	return false // allow window close → app exit
}

// defaultLang returns the default language code from config.
func (a *App) defaultLang() string {
	if a.cfg != nil && a.cfg.Multilingual.DefaultLang != "" {
		return a.cfg.Multilingual.DefaultLang
	}
	return "en"
}

// fixSignals patches ONNX Runtime / WebKit signal handlers that lack SA_ONSTACK.
// Called defensively on every frontend-bound method because WebKit/JSC can
// reinstall the SIGSEGV handler after Startup() returns.
func (a *App) fixSignals() { sigfix.AfterSherpa() }

// ListAudioDevices returns available audio input devices.
func (a *App) ListAudioDevices() ([]audio.DeviceInfo, error) {
	a.fixSignals()
	return audio.ListDevices()
}

// ListMonitorSources returns available monitor (system audio) sources.
func (a *App) ListMonitorSources() ([]audio.DeviceInfo, error) {
	a.fixSignals()
	return audio.ListMonitorSources()
}

// SystemAudioMode reports which kind of second-audio-source picker the
// frontend should show: "manual" (Linux — pick a PulseAudio monitor
// source from ListMonitorSources) or "auto" (macOS — pick from
// ListAudioSources instead, a live list of apps currently producing
// audio, plus an always-present "Everything"; see StartSession's
// monitorDevice argument, which on macOS carries that selection rather
// than a PulseAudio device name).
func (a *App) SystemAudioMode() string {
	a.fixSignals()
	if runtime.GOOS == "darwin" {
		return "auto"
	}
	return "manual"
}

// AudioSourceView is the camelCase JSON view of one selectable macOS
// audio source for the frontend's picker.
type AudioSourceView struct {
	ID   string `json:"id"`   // "everything", or a decimal PID -- pass straight back as StartSession's monitorDevice
	Name string `json:"name"` // human-readable, e.g. "Everything" or "Microsoft Teams"
}

// ListAudioSources returns macOS's second-audio-source picker options:
// "Everything" (always first, whole-system audio, no speaker
// diarization — see live.Config.SkipMonitorDiarization) followed by
// every app currently producing audio output
// (internal/audiosources.ListActive). Empty (not an error) on Linux,
// where SystemAudioMode() already tells the frontend to use
// ListMonitorSources instead, and where "everything" isn't a capturable
// source.
func (a *App) ListAudioSources() ([]AudioSourceView, error) {
	a.fixSignals()
	if runtime.GOOS != "darwin" {
		return []AudioSourceView{}, nil
	}
	out := []AudioSourceView{{ID: "everything", Name: "Everything"}}

	active, err := audiosources.ListActive()
	if err != nil {
		return out, nil // "Everything" is still a valid answer even if enumeration failed
	}
	for _, src := range active {
		out = append(out, AudioSourceView{
			ID:   strconv.Itoa(src.PID),
			Name: src.Name,
		})
	}
	return out, nil
}

// StartSession begins a new live transcription session.
// platform is optional — set by auto-detect for meeting title (e.g. "Teams").
func (a *App) StartSession(micDevice, monitorDevice, lang, platform string) error {
	a.fixSignals()
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.shuttingDown {
		return fmt.Errorf("shutting down")
	}

	if a.recording {
		return fmt.Errorf("session already in progress")
	}

	bundle, release, err := a.leaseEnginesLocked("recording")
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			// a.mu is still held here (StartSession's own deferred
			// Unlock runs after this), and release takes it: give the
			// lease back directly instead.
			a.releaseLeaseLocked("recording")
		}
	}()

	if lang == "" {
		lang = bundle.engines.DefaultLang()
	}

	status := bundle.modelMgr.Check()

	cfg := live.Config{
		Engine:            bundle.engines.Get(lang),
		Embedder:          meetingEmbedder(a.cfg, status, bundle.embedders, lang),
		Tracker:           a.tracker,
		VADPath:           status.VADPath,
		SegmentBufferSize: 64,
	}
	// Realtime pass is English-only (see internal/transcribe's
	// StreamingEngine); other languages keep today's single-pass path.
	if lang == "en" {
		cfg.StreamingEngine = bundle.streamingEngine
	}

	// Set up mic capturer
	if micDevice != "" {
		capturer, err := audio.NewCapturer(micDevice, audio.Input)
		if err != nil {
			return fmt.Errorf("creating mic capturer: %w", err)
		}
		cfg.MicCapturer = audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128)
	}

	// Set up monitor capturer (optional) — the second audio source
	// (a PulseAudio monitor device on Linux, chosen from
	// ListMonitorSources; a specific app or "everything" on macOS,
	// chosen from ListAudioSources — see internal/meetingaudio).
	monCapturer, err := meetingaudio.NewMonitorSource(monitorDevice)
	if err != nil {
		if cfg.MicCapturer != nil {
			cfg.MicCapturer.Close()
		}
		return fmt.Errorf("creating monitor capturer: %w", err)
	}
	if monCapturer != nil {
		cfg.MonitorCapturer = monCapturer
		cfg.SkipMonitorDiarization = monitorDevice == "everything"
	}

	// Reset speaker tracker for new session
	if a.tracker != nil {
		a.tracker.Reset()
	}

	coordinator := live.New(cfg)
	if err := coordinator.Start(a.ctx); err != nil {
		if cfg.MicCapturer != nil {
			cfg.MicCapturer.Close()
		}
		if cfg.MonitorCapturer != nil {
			cfg.MonitorCapturer.Close()
		}
		return fmt.Errorf("starting coordinator: %w", err)
	}

	// Re-grab hotkeys — audio device init can interfere with X11 key grabs
	hotkey.ReGrabAll()

	// Create session
	var sources []string
	if micDevice != "" {
		sources = append(sources, "mic")
	}
	if cfg.MonitorCapturer != nil {
		sources = append(sources, "monitor")
	}

	title := fmt.Sprintf("Session %s", time.Now().Format("2006-01-02 15:04"))
	if platform != "" {
		title = fmt.Sprintf("%s Meeting %s", platform, time.Now().Format("2006-01-02 15:04"))
	}

	a.currentSess = &session.Session{
		ID:        uuid.New().String(),
		Title:     title,
		Platform:  platform,
		Language:  lang,
		CreatedAt: time.Now(),
		Sources:   sources,
	}

	a.coordinator = coordinator
	a.recording = true
	started = true
	a.sessionRelease = release

	// Screen-based speaker-label hints (macOS only; no-op on Linux —
	// see internal/videohint). Its own context, not the app-lifetime
	// a.ctx, since it must stop when this session does — same
	// reasoning as dictCancel above.
	videoHintCtx, videoHintCancel := context.WithCancel(a.ctx)
	a.videoHintCancel = videoHintCancel
	a.videoHintMu.Lock()
	a.videoHintActivity = nil
	a.videoHintMu.Unlock()
	videoHintEvents := make(chan videohint.Event, 32)
	pollInterval, triggerDebounce := a.cfg.Meeting.VideoHintTiming()
	go videohint.Poll(videoHintCtx, pollInterval, triggerDebounce, coordinator.HintNeeded(), videoHintEvents)
	go a.emitVideoHintEvents(videoHintCtx, videoHintEvents)

	// Start emitting segments to frontend
	a.segmentsDone = a.emitSessionSegments(coordinator.Segments(), coordinator.SegmentUpdates(), a.currentSess)

	wailsRuntime.EventsEmit(a.ctx, "session:started", a.currentSess.ID)
	return nil
}

// StopSession stops the current live transcription session and saves it.
// Returns immediately after stopping the coordinator; audio encoding and
// session saving happen in the background so the UI stays responsive.
func (a *App) StopSession() (*session.Session, error) {
	a.fixSignals()
	a.mu.Lock()

	if !a.recording || a.coordinator == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("no session in progress")
	}
	// Counted from claiming the session until its save is queued; see stopWG.
	a.stopWG.Add(1)
	defer a.stopWG.Done()

	coordinator := a.coordinator
	sess := a.currentSess
	videoHintCancel := a.videoHintCancel
	segmentsDone := a.segmentsDone
	release := a.sessionRelease
	a.sessionRelease = nil
	a.recording = false
	a.currentSess = nil
	a.coordinator = nil
	a.videoHintCancel = nil
	a.segmentsDone = nil
	a.mu.Unlock()

	if videoHintCancel != nil {
		videoHintCancel()
	}

	// Stop coordinator (waits for pipeline flush — typically < 1s)
	fmt.Printf("session %s: stopping capture\n", sess.ID)
	coordinator.Stop()
	fmt.Printf("session %s: capture stopped, queueing save\n", sess.ID)

	// Finalize timestamps
	sess.EndedAt = time.Now()
	sess.Duration = sess.EndedAt.Sub(sess.CreatedAt).Seconds()

	// Notify UI immediately — recording is done
	wailsRuntime.EventsEmit(a.ctx, "session:stopped", sess.ID)

	// Hand off to the serial save worker so the next StartSession can
	// proceed immediately while encoding + diarization run in the background.
	a.saveQueue <- &saveRequest{sess: sess, coordinator: coordinator, segmentsDone: segmentsDone, release: release}

	return sess, nil
}

// saveWorker drains the save queue and persists each session serially.
// Must be the only goroutine calling persistSession.
func (a *App) saveWorker() {
	defer a.saveWG.Done()
	for req := range a.saveQueue {
		a.persistSession(req)
	}
}

// persistSession encodes audio, persists the session, then runs
// diarization as a refinement pass. Saving before diarization ensures
// the session is recoverable even if diarization or the app crashes.
func (a *App) persistSession(req *saveRequest) {
	sess := req.sess
	coordinator := req.coordinator
	// The engine lease covers pass-2 refinement only: once the final
	// segments are in, the rest of the save (audio encoding, JSON,
	// diarization in its own process) doesn't touch the engines, and
	// holding the lease through diarization blocked settings changes for
	// as long as that took. release is idempotent, so the deferred call
	// is just a backstop.
	if req.release != nil {
		defer req.release()
	}

	// Pass-2 refinements still queued at stop keep arriving after
	// StopSession returns; save only once they have all been applied, so
	// the saved text is the refined text and no segment is left "pending".
	if req.segmentsDone != nil {
		fmt.Printf("session %s: waiting for final segments\n", sess.ID)
		<-req.segmentsDone
	}
	if req.release != nil {
		req.release()
	}
	fmt.Printf("session %s: saving\n", sess.ID)

	var tracks [][]float32
	if coordinator.IsDualSource() {
		mic := coordinator.MicSamples()
		mon := coordinator.MonitorSamples()
		if len(mic) > 0 && len(mon) > 0 {
			tracks = [][]float32{mic, mon}
		}
	} else {
		samples := coordinator.AudioSamples()
		if len(samples) > 0 {
			tracks = [][]float32{samples}
		}
	}
	if len(tracks) > 0 {
		audioPath := filepath.Join(config.SessionDir(), sess.ID, "audio.m4a")
		if err := session.SaveAudioM4A(tracks, 16000, audioPath); err == nil {
			sess.AudioPath = audioPath
		} else {
			fmt.Printf("Error saving audio: %v\n", err)
		}
	}

	// Persist before diarization so the session survives a diarize crash.
	if err := a.store.Save(sess); err != nil {
		fmt.Printf("Error saving session: %v\n", err)
	}

	// Refinement: neural diarization in an isolated subprocess (sibling
	// `tomoe` binary) so sherpa-onnx Process() crashes never reach the
	// GUI. Retries GPU → GPU → CPU; on success the subprocess overwrites
	// session.json with refined labels. We don't reload here because
	// the frontend will re-fetch via LoadSession on the session:saved
	// event below.
	a.mu.Lock()
	modelMgr := a.bundle.modelMgr
	a.mu.Unlock()
	if modelMgr != nil && modelMgr.Check().DiarizationReady() {
		if err := session.RunDiarizeWithRetry(sess.ID, nil); err != nil {
			fmt.Printf("Warning: diarization failed (session saved without refinement): %v\n", err)
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "session:saved", sess.ID)
}

// GetSessionList returns all stored sessions.
func (a *App) GetSessionList() ([]*session.Session, error) {
	a.fixSignals()
	if a.store == nil {
		return nil, nil
	}
	return a.store.List()
}

// LoadSession returns a stored session by ID.
func (a *App) LoadSession(id string) (*session.Session, error) {
	a.fixSignals()
	if a.store == nil {
		return nil, fmt.Errorf("session store not initialized")
	}
	return a.store.Load(id)
}

// ExportSession exports a session in the specified format and returns the content.
func (a *App) ExportSession(id, format string) (string, error) {
	a.fixSignals()
	sess, err := a.store.Load(id)
	if err != nil {
		return "", err
	}

	var buf []byte
	w := &bytesWriter{buf: &buf}

	switch format {
	case "markdown":
		err = session.ExportMarkdown(sess, w)
	case "text":
		err = session.ExportPlainText(sess, w)
	case "srt":
		err = session.ExportSRT(sess, w)
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	if err != nil {
		return "", err
	}

	return string(buf), nil
}

// UpdateSession updates a session's title and/or platform.
func (a *App) UpdateSession(id, title, platform string) error {
	a.fixSignals()
	if a.store == nil {
		return fmt.Errorf("session store not initialized")
	}
	sess, err := a.store.Load(id)
	if err != nil {
		return err
	}
	if title != "" {
		sess.Title = title
	}
	if platform != "" {
		sess.Platform = platform
	}
	return a.store.Save(sess)
}

// DeleteSession deletes a session by ID.
func (a *App) DeleteSession(id string) error {
	a.fixSignals()
	return a.store.Delete(id)
}

// GetConfig returns the current configuration.
func (a *App) GetConfig() *config.Config {
	a.fixSignals()
	return a.cfg
}

// GetGPUInfo returns GPU detection info.
func (a *App) GetGPUInfo() *gpu.Info {
	a.fixSignals()
	return gpu.Detect()
}

// GetModelStatus returns the model download status.
func (a *App) GetModelStatus() *models.Status {
	a.fixSignals()
	a.mu.Lock()
	mgr := a.bundle.modelMgr
	a.mu.Unlock()
	if mgr == nil {
		return &models.Status{}
	}
	return mgr.Check()
}

// IsRecording returns whether a session is currently recording.
func (a *App) IsRecording() bool {
	a.fixSignals()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recording
}

// GetAvailableLanguages returns the list of configured language codes.
// Uses config as source of truth (not engine availability) so the UI
// always shows all configured languages.
func (a *App) GetAvailableLanguages() []string {
	a.fixSignals()
	if a.cfg != nil && a.cfg.Multilingual.Enabled && len(a.cfg.Multilingual.Languages) > 0 {
		return a.cfg.Multilingual.Languages
	}
	return []string{"en"}
}

// GetDefaultLanguage returns the default language code.
func (a *App) GetDefaultLanguage() string {
	a.fixSignals()
	a.mu.Lock()
	engines := a.bundle.engines
	a.mu.Unlock()
	if engines == nil {
		return "en"
	}
	return engines.DefaultLang()
}

// RetranscribeSession re-transcribes a saved session's audio with a different language.
// Runs in a background goroutine so the UI stays responsive.
func (a *App) RetranscribeSession(id, lang string) error {
	a.fixSignals()
	if a.store == nil {
		return fmt.Errorf("session store not initialized")
	}
	a.mu.Lock()
	bundle, release, err := a.leaseEnginesLocked("re-transcription")
	a.mu.Unlock()
	if err != nil {
		return err
	}
	leased := false
	defer func() {
		if !leased {
			release()
		}
	}()

	sess, err := a.store.Load(id)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}
	if sess.AudioPath == "" {
		return fmt.Errorf("session has no saved audio")
	}

	engine := bundle.engines.Get(lang)

	leased = true
	go func() {
		defer release()
		// Decode audio to PCM float32
		samples, err := session.DecodeToFloat32(sess.AudioPath)
		if err != nil {
			fmt.Printf("Re-transcribe: decode error: %v\n", err)
			wailsRuntime.EventsEmit(a.ctx, "session:retranscribe:error", err.Error())
			return
		}

		// Transcribe with VAD segmentation
		result, err := engine.TranscribeSamples(samples)
		if err != nil {
			fmt.Printf("Re-transcribe: transcription error: %v\n", err)
			wailsRuntime.EventsEmit(a.ctx, "session:retranscribe:error", err.Error())
			return
		}

		// Replace segments with re-transcribed result
		sess.Language = lang
		sess.Segments = []session.Segment{
			{
				ID:       "retranscribed-1",
				Speaker:  "You",
				Text:     result.Text,
				Language: lang,
			},
		}

		if err := a.store.Save(sess); err != nil {
			fmt.Printf("Re-transcribe: save error: %v\n", err)
			wailsRuntime.EventsEmit(a.ctx, "session:retranscribe:error", err.Error())
			return
		}

		fmt.Printf("Re-transcribed session %s in %s\n", id, lang)
		wailsRuntime.EventsEmit(a.ctx, "session:retranscribed", id)
	}()

	return nil
}

// bytesWriter is a simple io.Writer that appends to a byte slice.
type bytesWriter struct {
	buf *[]byte
}

func (w *bytesWriter) Write(p []byte) (n int, err error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// errApplyingSettings is returned while ApplySettings reloads the engines.
var errApplyingSettings = fmt.Errorf("settings are being applied; try again in a moment")

// leaseEnginesLocked returns the current engine bundle for one decoding
// job (a recording, dictation, save or re-transcription) and counts it as
// in use until release is called; reloadEngines won't swap the bundle
// while any lease is out. release is safe to call more than once. Caller
// must hold a.mu.
func (a *App) leaseEnginesLocked(what string) (bundle engineBundle, release func(), err error) {
	if a.reconfiguring {
		return engineBundle{}, nil, errApplyingSettings
	}
	if a.bundle.engines == nil {
		return engineBundle{}, nil, fmt.Errorf("transcription engine not initialized (models may not be downloaded; see Tools)")
	}
	a.engineLeases++
	fmt.Printf("engines: leased for %s (%d in use)\n", what, a.engineLeases)
	var once sync.Once
	return a.bundle, func() {
		once.Do(func() {
			a.mu.Lock()
			a.releaseLeaseLocked(what)
			a.mu.Unlock()
		})
	}, nil
}

// releaseLeaseLocked gives back one engine lease. Caller must hold a.mu;
// normally reached through the release func from leaseEnginesLocked.
func (a *App) releaseLeaseLocked(what string) {
	a.engineLeases--
	fmt.Printf("engines: released from %s (%d in use)\n", what, a.engineLeases)
}
