package backend

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	icalendar "github.com/sosuke-ai/tomoe-pc/internal/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/calendar/sock"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/gpu"
	"github.com/sosuke-ai/tomoe-pc/internal/hotkey"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// App is the Wails backend, bound to the frontend via bindings.
type App struct {
	ctx context.Context
	cfg *config.Config

	engines     *transcribe.EngineSet
	embedder    *speaker.Embedder
	tracker     *speaker.Tracker
	coordinator *live.Coordinator
	store       *session.Store
	modelMgr    *models.Manager
	detector    *meeting.Detector

	// Calendar enrichment. calendar is the pluggable Enricher — nil disables
	// enrichment entirely. calendarResolver is an optional post-match hook
	// that can enrich the participant list on a matched event (e.g. overlay
	// canonical identity from a directory); nil means "keep whatever the
	// enricher produced". calendarStore is the ephemeral per-session cache
	// under $XDG_STATE_HOME/tomoe/calendar. All are populated in Startup
	// unless an external embedder called SetCalendar / SetCalendarParticipantResolver first.
	calendar         calendar.Enricher
	calendarResolver calendar.ParticipantResolver
	calendarStore    *icalendar.Store

	mu                 sync.Mutex
	recording          bool // meeting recording in progress
	dictating          bool // dictation recording in progress
	dictCoordinator    *live.Coordinator
	dictCancel         context.CancelFunc
	currentSess        *session.Session
	currentWindowTitle string // captured at MeetingStarted; travels to persistSession via saveRequest

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
}

// saveRequest is a unit of work for the background save worker.
type saveRequest struct {
	sess        *session.Session
	coordinator *live.Coordinator
	// windowTitle is a snapshot captured at StartSession time (from
	// MeetingEvent.WindowTitle). Used by calendar enrichment to extract a
	// meeting URL. Empty when no window title was available.
	windowTitle string
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

// SetCalendar installs a calendar.Enricher for post-save enrichment. External
// projects that embed Tomoe call this before Startup to plug in their own
// resolver — e.g. a directory service or scheduling system in addition to (or
// in place of) the built-in ICS provider. When called after Startup, the new
// enricher takes effect on the next persistSession call.
//
// Passing nil disables calendar enrichment entirely.
func (a *App) SetCalendar(e calendar.Enricher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calendar = e
}

// SetCalendarParticipantResolver installs an optional post-match hook that
// enriches the participant list on a matched event before it is cached and
// surfaced to the frontend. See calendar.ParticipantResolver for the
// contract.
//
// The resolver runs regardless of which Enricher produced the match; it is
// composable with SetCalendar. Passing nil removes the hook.
func (a *App) SetCalendarParticipantResolver(r calendar.ParticipantResolver) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calendarResolver = r
}

// Startup is called by Wails when the application starts.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	// Start the background save worker. Drains pending saves on Shutdown
	// so we never lose a session that was queued before app exit.
	a.saveWG.Add(1)
	go a.saveWorker()

	// Load or create config
	var cfg *config.Config
	if config.Exists() {
		var err error
		cfg, err = config.Load(config.Path())
		if err != nil {
			cfg = config.DefaultConfig()
		}
	} else {
		cfg = config.DefaultConfig()
	}
	a.cfg = cfg

	// Initialize session store first — it has no heavy dependencies
	a.store = session.NewStore(config.SessionDir())

	// Initialize model manager
	a.modelMgr = models.NewManager(cfg.Transcription.ModelPath)
	status := a.modelMgr.Check()

	// Create transcription engine if models are ready
	if status.Ready() {
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
		}, status, &cfg.Multilingual)
		if err == nil {
			a.engines = engines
		}
	}

	// Create speaker embedder if available
	if status.SpeakerEmbeddingReady {
		embedder, err := speaker.NewEmbedder(status.SpeakerEmbeddingPath)
		if err == nil {
			a.embedder = embedder
			threshold := speaker.DefaultThreshold
			if cfg.Meeting.SpeakerThreshold > 0 {
				threshold = cfg.Meeting.SpeakerThreshold
			}
			a.tracker = speaker.NewTracker(threshold)
		}
	}

	// Start meeting auto-detector if enabled
	if cfg.Meeting.AutoDetect {
		a.detector = meeting.NewDetector()
		if err := a.detector.Start(ctx); err != nil {
			fmt.Printf("Warning: meeting auto-detect unavailable: %v\n", err)
			a.detector = nil
		}
	}

	// Calendar enrichment. Always construct the local cache store so
	// existing cached matches can be surfaced. Only build the built-in
	// enricher when calendar is enabled AND no external embedder has
	// already installed one via SetCalendar.
	a.calendarStore = icalendar.NewStore(icalendar.DefaultStoreDir())
	if a.calendar == nil && cfg.Calendar.Enabled {
		enricher, err := buildCalendarEnricher(cfg.Calendar)
		if err != nil {
			fmt.Printf("Warning: calendar enrichment disabled: %v\n", err)
		} else {
			a.calendar = enricher
		}
	}
	// Optional out-of-process participant resolver. Same "external
	// embedder wins" precedence as the Enricher.
	if a.calendarResolver == nil && cfg.Calendar.Enabled {
		resolver, err := sock.New(cfg.Calendar.ParticipantResolver)
		if err != nil {
			fmt.Printf("Warning: participant resolver disabled: %v\n", err)
		} else if resolver != nil {
			a.calendarResolver = resolver
		}
	}

	// Start system tray (after engines are loaded so language menus are correct)
	StartTrayAsync(a)

	// Register meeting hotkey
	if err := a.registerHotkeys(); err != nil {
		// Non-fatal — hotkey may not be available in all environments
		fmt.Printf("Warning: could not register meeting hotkey: %v\n", err)
	}
}

// Shutdown is called by Wails when the application is closing.
func (a *App) Shutdown(ctx context.Context) {
	// Snapshot mutable fields under lock before acting on them.
	a.mu.Lock()
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

	// Drain pending saves before closing engines/embedder, since a save in
	// flight may still be using sherpa-onnx state.
	if a.saveQueue != nil {
		close(a.saveQueue)
		a.saveWG.Wait()
		a.saveQueue = nil
	}

	if a.engines != nil {
		a.engines.Close()
	}
	if a.embedder != nil {
		a.embedder.Close()
	}
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

// StartSession begins a new live transcription session.
//
// platform is optional — set by auto-detect for meeting title (e.g. "Teams").
// windowTitle is optional — captured by auto-detect (X11 only via xdotool),
// used by calendar match enrichment to extract a meeting URL. Pass "" from
// manual paths.
func (a *App) StartSession(micDevice, monitorDevice, lang, platform, windowTitle string) error {
	a.fixSignals()
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.recording {
		return fmt.Errorf("session already in progress")
	}

	if a.engines == nil {
		return fmt.Errorf("transcription engine not initialized (models may not be downloaded)")
	}

	if lang == "" {
		lang = a.engines.DefaultLang()
	}

	status := a.modelMgr.Check()

	cfg := live.Config{
		Engine:            a.engines.Get(lang),
		Embedder:          a.embedder,
		Tracker:           a.tracker,
		VADPath:           status.VADPath,
		SegmentBufferSize: 64,
	}

	// Set up mic capturer
	if micDevice != "" {
		capturer, err := audio.NewCapturer(micDevice, audio.Input)
		if err != nil {
			return fmt.Errorf("creating mic capturer: %w", err)
		}
		cfg.MicCapturer = audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128)
	}

	// Set up monitor capturer
	if monitorDevice != "" {
		capturer, err := audio.NewCapturer(monitorDevice, audio.Monitor)
		if err != nil {
			if cfg.MicCapturer != nil {
				cfg.MicCapturer.Close()
			}
			return fmt.Errorf("creating monitor capturer: %w", err)
		}
		cfg.MonitorCapturer = audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128)
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
	if monitorDevice != "" {
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
	a.currentWindowTitle = windowTitle

	a.coordinator = coordinator
	a.recording = true

	// Start emitting segments to frontend
	go a.emitSegments()

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

	coordinator := a.coordinator
	sess := a.currentSess
	windowTitle := a.currentWindowTitle
	a.recording = false
	a.currentSess = nil
	a.currentWindowTitle = ""
	a.coordinator = nil
	a.mu.Unlock()

	// Stop coordinator (waits for pipeline flush — typically < 1s)
	coordinator.Stop()

	// Finalize timestamps
	sess.EndedAt = time.Now()
	sess.Duration = sess.EndedAt.Sub(sess.CreatedAt).Seconds()

	// Notify UI immediately — recording is done
	wailsRuntime.EventsEmit(a.ctx, "session:stopped", sess.ID)

	// Hand off to the serial save worker so the next StartSession can
	// proceed immediately while encoding + diarization run in the background.
	a.saveQueue <- &saveRequest{sess: sess, coordinator: coordinator, windowTitle: windowTitle}

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
	if a.modelMgr != nil && a.modelMgr.Check().DiarizationReady() {
		if err := session.RunDiarizeWithRetry(sess.ID, nil); err != nil {
			fmt.Printf("Warning: diarization failed (session saved without refinement): %v\n", err)
		}
	}

	// Calendar enrichment — writes to the ephemeral local cache; never
	// touches session.json. Failure is logged and swallowed.
	a.runCalendarEnrichment(sess, req.windowTitle)

	wailsRuntime.EventsEmit(a.ctx, "session:saved", sess.ID)
}

// GetSessionList returns all stored sessions, augmented with any cached
// calendar event via the ephemeral local cache. The calendar_event field is a
// serialization-time enrichment — never part of session.json on disk.
func (a *App) GetSessionList() ([]*sessionWithCalendar, error) {
	a.fixSignals()
	if a.store == nil {
		return nil, nil
	}
	sessions, err := a.store.List()
	if err != nil {
		return nil, err
	}
	return a.withCalendarSlice(sessions), nil
}

// LoadSession returns a stored session by ID, augmented with any cached
// calendar event. See GetSessionList.
func (a *App) LoadSession(id string) (*sessionWithCalendar, error) {
	a.fixSignals()
	if a.store == nil {
		return nil, fmt.Errorf("session store not initialized")
	}
	sess, err := a.store.Load(id)
	if err != nil {
		return nil, err
	}
	return a.withCalendar(sess), nil
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
	return a.modelMgr.Check()
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
	if a.engines == nil {
		return "en"
	}
	return a.engines.DefaultLang()
}

// RetranscribeSession re-transcribes a saved session's audio with a different language.
// Runs in a background goroutine so the UI stays responsive.
func (a *App) RetranscribeSession(id, lang string) error {
	a.fixSignals()
	if a.store == nil {
		return fmt.Errorf("session store not initialized")
	}
	if a.engines == nil {
		return fmt.Errorf("transcription engine not initialized")
	}

	sess, err := a.store.Load(id)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}
	if sess.AudioPath == "" {
		return fmt.Errorf("session has no saved audio")
	}

	engine := a.engines.Get(lang)

	go func() {
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

		// Refresh the calendar cache entry when it is missing or has no
		// matched event. Skips when a prior enrichment already succeeded
		// so a re-transcribe cannot unlink a match the user may have
		// relied on.
		if a.calendarStore != nil {
			existing, _ := a.calendarStore.Load(id)
			if existing == nil || existing.CalendarEvent == nil {
				windowTitle := ""
				if existing != nil {
					windowTitle = existing.WindowTitle
				}
				a.runCalendarEnrichment(sess, windowTitle)
			}
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
