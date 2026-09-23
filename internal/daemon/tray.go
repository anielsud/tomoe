package daemon

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"fyne.io/systray"
)

// daemonTray manages the system tray icon for the CLI daemon.
type daemonTray struct {
	quitCh      chan struct{}
	dictationCh chan string // carries language code; "" = stop
	meetingCh   chan string // carries language code; "" = stop
	ready       atomic.Bool

	mDictation     *systray.MenuItem // start item (with sub-menus in multilingual mode)
	mMeeting       *systray.MenuItem // start item (with sub-menus in multilingual mode)
	mStopDictation *systray.MenuItem // flat stop item (nil in single-lang mode)
	mStopMeeting   *systray.MenuItem // flat stop item (nil in single-lang mode)
}

// newDaemonTray allocates a daemonTray's channels without touching
// systray itself. Callers drive the actual systray.Run(...) call — see
// startDaemonTray (Linux/other) and runWithTray in run_linux.go /
// run_darwin.go, which differ in *where* that call needs to happen.
func newDaemonTray() *daemonTray {
	return &daemonTray{
		quitCh:      make(chan struct{}, 1),
		dictationCh: make(chan string, 1),
		meetingCh:   make(chan string, 1),
	}
}

// startDaemonTray starts the system tray in a goroutine.
// languages is the list of available language codes (e.g. ["en", "bn"]).
// defaultLang is the fallback language code.
func startDaemonTray(languages []string, defaultLang string) *daemonTray {
	t := newDaemonTray()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Warning: system tray unavailable: %v\n", r)
			}
		}()
		systray.Run(func() { t.onReady(languages, defaultLang) }, func() {})
	}()
	return t
}

// onReady builds the tray's menu. Must run on whatever goroutine/thread
// systray.Run's onReady callback actually invokes it from.
func (t *daemonTray) onReady(languages []string, defaultLang string) {
	systray.SetTitle("Tomoe")
	systray.SetTooltip("Tomoe — Ready")
	systray.SetIcon(daemonTrayIcon)

	if len(languages) > 1 {
		t.initMultilingual(languages)
	} else {
		t.initSingleLang(defaultLang)
	}

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop Tomoe daemon")
	t.ready.Store(true)

	go func() {
		<-mQuit.ClickedCh
		select {
		case t.quitCh <- struct{}{}:
		default:
		}
	}()
}

// initSingleLang creates simple dictation/meeting items for a single language.
func (t *daemonTray) initSingleLang(lang string) {
	t.mDictation = systray.AddMenuItem("Start Dictation", "Start/Stop dictation")
	t.mMeeting = systray.AddMenuItem("Start Meeting", "Start/Stop meeting recording")

	go func() {
		for {
			select {
			case <-t.mDictation.ClickedCh:
				select {
				case t.dictationCh <- lang:
				default:
				}
			case <-t.mMeeting.ClickedCh:
				select {
				case t.meetingCh <- lang:
				default:
				}
			}
		}
	}()
}

// initMultilingual creates parent items with per-language sub-items for start,
// and flat stop items that are shown only when recording is active.
func (t *daemonTray) initMultilingual(languages []string) {
	t.mDictation = systray.AddMenuItem("Dictation", "Start dictation")
	for _, lang := range languages {
		sub := t.mDictation.AddSubMenuItem(
			fmt.Sprintf("Start Dictation - %s", strings.ToUpper(lang)),
			fmt.Sprintf("Dictate in %s", lang),
		)
		go func(l string, item *systray.MenuItem) {
			for range item.ClickedCh {
				select {
				case t.dictationCh <- l:
				default:
				}
			}
		}(lang, sub)
	}

	// Flat stop item — hidden until dictation is active
	t.mStopDictation = systray.AddMenuItem("Stop Dictation", "Stop dictation")
	t.mStopDictation.Hide()
	go func() {
		for range t.mStopDictation.ClickedCh {
			select {
			case t.dictationCh <- "":
			default:
			}
		}
	}()

	t.mMeeting = systray.AddMenuItem("Meeting", "Start meeting recording")
	for _, lang := range languages {
		sub := t.mMeeting.AddSubMenuItem(
			fmt.Sprintf("Start Meeting - %s", strings.ToUpper(lang)),
			fmt.Sprintf("Record meeting in %s", lang),
		)
		go func(l string, item *systray.MenuItem) {
			for range item.ClickedCh {
				select {
				case t.meetingCh <- l:
				default:
				}
			}
		}(lang, sub)
	}

	// Flat stop item — hidden until meeting is active
	t.mStopMeeting = systray.AddMenuItem("Stop Meeting", "Stop meeting recording")
	t.mStopMeeting.Hide()
	go func() {
		for range t.mStopMeeting.ClickedCh {
			select {
			case t.meetingCh <- "":
			default:
			}
		}
	}()
}

func (t *daemonTray) SetIdle() {
	if !t.ready.Load() {
		return
	}
	systray.SetTooltip("Tomoe — Ready")
	systray.SetIcon(daemonTrayIcon)
	if t.mDictation != nil {
		t.mDictation.Show()
	}
	if t.mStopDictation != nil {
		t.mStopDictation.Hide()
	}
	if t.mMeeting != nil {
		t.mMeeting.Show()
	}
	if t.mStopMeeting != nil {
		t.mStopMeeting.Hide()
	}
}

func (t *daemonTray) SetDictating() {
	if !t.ready.Load() {
		return
	}
	systray.SetTooltip("Tomoe — Dictating...")
	systray.SetIcon(daemonTrayIconRecording)
	// Hide start items, show flat stop
	if t.mDictation != nil {
		t.mDictation.Hide()
	}
	if t.mStopDictation != nil {
		t.mStopDictation.Show()
	}
	if t.mMeeting != nil {
		t.mMeeting.Hide()
	}
}

func (t *daemonTray) SetMeetingRecording() {
	if !t.ready.Load() {
		return
	}
	systray.SetTooltip("Tomoe — Meeting Recording...")
	systray.SetIcon(daemonTrayIconRecording)
	// Hide start items, show flat stop
	if t.mMeeting != nil {
		t.mMeeting.Hide()
	}
	if t.mStopMeeting != nil {
		t.mStopMeeting.Show()
	}
	if t.mDictation != nil {
		t.mDictation.Hide()
	}
}

func (t *daemonTray) Close() {
	if t.ready.Load() {
		systray.Quit()
	}
}
