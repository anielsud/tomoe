package backend

import (
	"context"
	"fmt"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/clipboard"
	"github.com/sosuke-ai/tomoe-pc/internal/hotkey"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// hotkeyManager manages the meeting hotkey in GUI mode.
type hotkeyManager struct {
	app      *App
	listener hotkey.Listener
	stop     <-chan struct{} // closed to end listen (see rebindHotkeys)
}

// dictationManager manages the dictation hotkey in GUI mode.
type dictationManager struct {
	app      *App
	listener hotkey.Listener
	clip     clipboard.Writer
	stop     <-chan struct{} // closed to end listen (see rebindHotkeys)
}

// registerHotkeys registers both the meeting and dictation hotkeys and
// starts their listen loops, recording in a.unregisterHotkeys how to undo
// all of it (see rebindHotkeys).
func (a *App) registerHotkeys() error {
	stop := make(chan struct{})
	var listeners []hotkey.Listener
	a.mu.Lock()
	a.unregisterHotkeys = func() {
		close(stop)
		for _, l := range listeners {
			_ = l.Unregister()
		}
	}
	a.mu.Unlock()

	// Register meeting hotkey
	meetingBinding := a.cfg.Hotkey.MeetingBinding
	if meetingBinding == "" {
		meetingBinding = "Super+Shift+X"
	}

	meetingListener, err := hotkey.NewListener(meetingBinding)
	if err != nil {
		return fmt.Errorf("creating meeting hotkey listener: %w", err)
	}

	if err := meetingListener.Register(); err != nil {
		return fmt.Errorf("registering meeting hotkey: %w", err)
	}
	listeners = append(listeners, meetingListener)

	mhk := &hotkeyManager{
		app:      a,
		listener: meetingListener,
		stop:     stop,
	}
	go mhk.listen()

	// Register dictation hotkey
	dictBinding := a.cfg.Hotkey.Binding
	if dictBinding == "" {
		dictBinding = "Super+Shift+S"
	}

	dictListener, err := hotkey.NewListener(dictBinding)
	if err != nil {
		fmt.Printf("Warning: could not create dictation hotkey listener: %v\n", err)
		return nil // meeting hotkey still works
	}

	if err := dictListener.Register(); err != nil {
		fmt.Printf("Warning: could not register dictation hotkey: %v\n", err)
		return nil
	}
	listeners = append(listeners, dictListener)

	dhk := &dictationManager{
		app:      a,
		listener: dictListener,
		clip:     clipboard.NewWriter(),
		stop:     stop,
	}
	go dhk.listen()

	return nil
}

// listen handles hotkey, tray, and auto-detect events for the meeting toggle.
func (hk *hotkeyManager) listen() {
	defaultLang := hk.app.defaultLang()

	// Get detector events channel (nil if auto-detect disabled)
	var detectCh <-chan meeting.MeetingEvent
	if hk.app.detector != nil {
		detectCh = hk.app.detector.Events()
	}

	for {
		select {
		case <-hk.stop:
			return
		case _, ok := <-hk.listener.Keydown():
			if !ok {
				return
			}
			hk.toggleMeeting(defaultLang, "")
		case lang := <-hk.app.trayMeetCh:
			hk.toggleMeeting(lang, "")
		case evt, ok := <-detectCh:
			if !ok {
				detectCh = nil
				continue
			}
			hk.handleDetectEvent(evt, defaultLang)
		}
	}
}

func (hk *hotkeyManager) toggleMeeting(lang, platform string) {
	hk.app.mu.Lock()
	recording := hk.app.recording
	dictating := hk.app.dictating
	hk.app.mu.Unlock()

	// Ignore if dictation is in progress
	if dictating {
		return
	}

	if recording {
		_, _ = hk.app.StopSession()
		if hk.app.tray != nil {
			hk.app.tray.setIdle()
		}
	} else if lang != "" {
		// Only start if a language is specified (empty = stop-only signal from tray)
		micDevice := hk.app.cfg.Audio.Device
		monitorDevice := hk.app.cfg.Meeting.MonitorDevice
		_ = hk.app.StartSession(micDevice, monitorDevice, lang, platform)
		if hk.app.tray != nil {
			hk.app.tray.setMeetingRecording()
		}
	} else {
		return
	}

	wailsRuntime.EventsEmit(hk.app.ctx, "hotkey:toggled", !recording)
}

// handleDetectEvent processes meeting auto-detect events.
func (hk *hotkeyManager) handleDetectEvent(evt meeting.MeetingEvent, defaultLang string) {
	hk.app.mu.Lock()
	recording := hk.app.recording
	dictating := hk.app.dictating
	hk.app.mu.Unlock()

	switch evt.Type {
	case meeting.MeetingStarted:
		if recording || dictating {
			return // already busy
		}
		platform := string(evt.Platform)
		micDevice := hk.app.cfg.Audio.Device
		monitorDevice := hk.app.cfg.Meeting.MonitorDevice
		if err := hk.app.StartSession(micDevice, monitorDevice, defaultLang, platform); err != nil {
			fmt.Printf("Auto-detect meeting start failed: %v\n", err)
			return
		}
		if hk.app.tray != nil {
			hk.app.tray.setMeetingRecording()
		}
		msg := fmt.Sprintf("%s meeting detected — recording started", platform)
		fmt.Println(msg)
		wailsRuntime.EventsEmit(hk.app.ctx, "meeting:autodetected", platform)
		wailsRuntime.EventsEmit(hk.app.ctx, "hotkey:toggled", true)

	case meeting.MeetingStopped:
		if !recording {
			return
		}
		_, _ = hk.app.StopSession()
		if hk.app.tray != nil {
			hk.app.tray.setIdle()
		}
		fmt.Println("Auto-detected meeting ended — recording stopped")
		wailsRuntime.EventsEmit(hk.app.ctx, "hotkey:toggled", false)
	}
}

// listen handles hotkey and tray events for dictation toggle.
func (dhk *dictationManager) listen() {
	defaultLang := dhk.app.defaultLang()
	for {
		select {
		case <-dhk.stop:
			return
		case _, ok := <-dhk.listener.Keydown():
			if !ok {
				return
			}
			dhk.toggleDictation(defaultLang)
		case lang := <-dhk.app.trayDictCh:
			dhk.toggleDictation(lang)
		}
	}
}

func (dhk *dictationManager) toggleDictation(lang string) {
	dhk.app.mu.Lock()
	recording := dhk.app.recording
	dictating := dhk.app.dictating
	dhk.app.mu.Unlock()

	if recording {
		return // ignore dictation while meeting active
	}

	if dictating {
		dhk.stopDictation()
	} else if lang != "" {
		// Only start if a language is specified (empty = stop-only signal from tray)
		dhk.startDictation(lang)
	}
}

func (dhk *dictationManager) startDictation(lang string) {
	device := dhk.app.cfg.Audio.Device
	if device == "" {
		device = "default"
	}

	micCapturer, err := audio.NewCapturer(device, audio.Input)
	if err != nil {
		fmt.Printf("Dictation: failed to create capturer: %v\n", err)
		return
	}

	dhk.app.mu.Lock()
	bundle, release, err := dhk.app.leaseEnginesLocked("dictation")
	dhk.app.mu.Unlock()
	if err != nil {
		fmt.Printf("Dictation: %v\n", err)
		micCapturer.Close()
		return
	}

	var vadPath string
	if bundle.modelMgr != nil {
		status := bundle.modelMgr.Check()
		vadPath = status.VADPath
	}

	cfg := live.Config{
		Engine:            bundle.engines.Get(lang),
		MicCapturer:       audio.NewStreamCapturer(micCapturer, audio.DefaultWindowSize, 128),
		VADPath:           vadPath,
		SegmentBufferSize: 32,
	}

	dictCtx, cancel := context.WithCancel(dhk.app.ctx)
	coordinator := live.New(cfg)
	if err := coordinator.Start(dictCtx); err != nil {
		fmt.Printf("Dictation: failed to start coordinator: %v\n", err)
		cfg.MicCapturer.Close()
		cancel()
		release()
		return
	}

	// Re-grab hotkeys — audio device init can interfere with X11 key grabs
	hotkey.ReGrabAll()

	dhk.app.mu.Lock()
	dhk.app.dictating = true
	dhk.app.dictCoordinator = coordinator
	dhk.app.dictCancel = cancel
	dhk.app.dictRelease = release
	dhk.app.mu.Unlock()

	if dhk.app.tray != nil {
		dhk.app.tray.setDictating()
	}
	wailsRuntime.EventsEmit(dhk.app.ctx, "dictation:started", nil)

	silenceTimeout := dhk.app.cfg.Output.SilenceTimeout
	if silenceTimeout <= 0 {
		silenceTimeout = 5.0
	}

	streamer := live.NewDictationStreamer(coordinator, live.DictationConfig{
		Clipboard:      dhk.clip,
		UseClipboard:   dhk.app.cfg.Output.Clipboard,
		AutoPaste:      dhk.app.cfg.Output.AutoPaste,
		SilenceTimeout: time.Duration(silenceTimeout * float64(time.Second)),
		OnSegment: func(seg session.Segment) {
			wailsRuntime.EventsEmit(dhk.app.ctx, "dictation:segment", seg.Text)
		},
		OnAutoStop: func() {
			wailsRuntime.EventsEmit(dhk.app.ctx, "dictation:done", "Auto-stopped (silence)")
			dhk.stopDictation()
		},
	})

	// Watch for streamer completion (coordinator stopped externally)
	go func() {
		<-streamer.Done()
	}()
}

func (dhk *dictationManager) stopDictation() {
	dhk.app.mu.Lock()
	coordinator := dhk.app.dictCoordinator
	cancel := dhk.app.dictCancel
	release := dhk.app.dictRelease
	dhk.app.dictRelease = nil
	dhk.app.dictating = false
	dhk.app.dictCoordinator = nil
	dhk.app.dictCancel = nil
	dhk.app.mu.Unlock()

	if coordinator != nil {
		coordinator.Stop()
	}
	if cancel != nil {
		cancel()
	}
	if release != nil {
		release() // only now: Stop waits for the last decode
	}

	if dhk.app.tray != nil {
		dhk.app.tray.setIdle()
	}
	wailsRuntime.EventsEmit(dhk.app.ctx, "dictation:stopped", nil)
	fmt.Println("Dictation stopped.")
}

// rebindHotkeys unregisters the current hotkeys and registers them again
// from a.cfg. Used when settings change the bindings, the default
// language or the meeting detector, all of which the listen loops capture
// when they start.
func (a *App) rebindHotkeys() error {
	a.mu.Lock()
	unregister := a.unregisterHotkeys
	a.unregisterHotkeys = nil
	a.mu.Unlock()
	if unregister != nil {
		unregister()
	}
	return a.registerHotkeys()
}
