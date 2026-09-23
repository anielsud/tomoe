package backend

import (
	"fmt"
	"strings"

	"fyne.io/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayManager handles the system tray icon and menu.
type trayManager struct {
	app            *App
	mDictation     *systray.MenuItem // start item (with sub-menus in multilingual mode)
	mMeeting       *systray.MenuItem // start item (with sub-menus in multilingual mode)
	mStopDictation *systray.MenuItem // flat stop item (nil in single-lang mode)
	mStopMeeting   *systray.MenuItem // flat stop item (nil in single-lang mode)
	mQuit          *systray.MenuItem
}

// StartTrayAsync starts the system tray. See tray_start_linux.go /
// tray_start_darwin.go — the two platforms need genuinely different
// wiring, not just a different backend, because darwin's Wails window
// already owns the real Cocoa main thread/run loop (see
// tray_start_darwin.go's doc comment for the full explanation).
func onTrayReady(app *App) {
	systray.SetTitle("Tomoe")
	systray.SetTooltip("Tomoe — Ready")
	systray.SetIcon(trayIcon)

	tm := &trayManager{app: app}
	app.tray = tm

	languages := app.GetAvailableLanguages()
	defaultLang := app.defaultLang()

	if len(languages) > 1 {
		tm.initMultilingual(languages)
	} else {
		tm.initSingleLang(defaultLang)
	}

	systray.AddSeparator()
	tm.mQuit = systray.AddMenuItem("Quit", "Quit Tomoe")

	go func() {
		<-tm.mQuit.ClickedCh
		if tm.app.recording {
			_, _ = tm.app.StopSession()
		}
		systray.Quit()
		if tm.app.ctx != nil {
			wailsRuntime.Quit(tm.app.ctx)
		}
	}()
}

// initSingleLang creates simple dictation/meeting items for a single language.
func (tm *trayManager) initSingleLang(lang string) {
	tm.mDictation = systray.AddMenuItem("Start Dictation", "Start/Stop dictation")
	tm.mMeeting = systray.AddMenuItem("Start Meeting", "Start/Stop meeting recording")

	go func() {
		for {
			select {
			case <-tm.mDictation.ClickedCh:
				if tm.app.ctx == nil {
					continue
				}
				select {
				case tm.app.trayDictCh <- lang:
				default:
				}
			case <-tm.mMeeting.ClickedCh:
				if tm.app.ctx == nil {
					continue
				}
				select {
				case tm.app.trayMeetCh <- lang:
				default:
				}
			}
		}
	}()
}

// initMultilingual creates parent items with per-language sub-items for start,
// and flat stop items that are shown only when recording is active.
func (tm *trayManager) initMultilingual(languages []string) {
	tm.mDictation = systray.AddMenuItem("Dictation", "Start dictation")
	for _, lang := range languages {
		sub := tm.mDictation.AddSubMenuItem(
			fmt.Sprintf("Start Dictation - %s", strings.ToUpper(lang)),
			fmt.Sprintf("Dictate in %s", lang),
		)
		go func(l string, item *systray.MenuItem) {
			for range item.ClickedCh {
				if tm.app.ctx == nil {
					continue
				}
				select {
				case tm.app.trayDictCh <- l:
				default:
				}
			}
		}(lang, sub)
	}

	// Flat stop item — hidden until dictation is active
	tm.mStopDictation = systray.AddMenuItem("Stop Dictation", "Stop dictation")
	tm.mStopDictation.Hide()
	go func() {
		for range tm.mStopDictation.ClickedCh {
			if tm.app.ctx == nil {
				continue
			}
			select {
			case tm.app.trayDictCh <- "":
			default:
			}
		}
	}()

	tm.mMeeting = systray.AddMenuItem("Meeting", "Start meeting recording")
	for _, lang := range languages {
		sub := tm.mMeeting.AddSubMenuItem(
			fmt.Sprintf("Start Meeting - %s", strings.ToUpper(lang)),
			fmt.Sprintf("Record meeting in %s", lang),
		)
		go func(l string, item *systray.MenuItem) {
			for range item.ClickedCh {
				if tm.app.ctx == nil {
					continue
				}
				select {
				case tm.app.trayMeetCh <- l:
				default:
				}
			}
		}(lang, sub)
	}

	// Flat stop item — hidden until meeting is active
	tm.mStopMeeting = systray.AddMenuItem("Stop Meeting", "Stop meeting recording")
	tm.mStopMeeting.Hide()
	go func() {
		for range tm.mStopMeeting.ClickedCh {
			if tm.app.ctx == nil {
				continue
			}
			select {
			case tm.app.trayMeetCh <- "":
			default:
			}
		}
	}()
}

func (tm *trayManager) setIdle() {
	systray.SetTooltip("Tomoe — Ready")
	systray.SetIcon(trayIcon)
	if tm.mDictation != nil {
		tm.mDictation.Show()
	}
	if tm.mStopDictation != nil {
		tm.mStopDictation.Hide()
	}
	if tm.mMeeting != nil {
		tm.mMeeting.Show()
	}
	if tm.mStopMeeting != nil {
		tm.mStopMeeting.Hide()
	}
}

func (tm *trayManager) setDictating() {
	systray.SetTooltip("Tomoe — Dictating...")
	systray.SetIcon(trayIconRecording)
	if tm.mDictation != nil {
		tm.mDictation.Hide()
	}
	if tm.mStopDictation != nil {
		tm.mStopDictation.Show()
	}
	if tm.mMeeting != nil {
		tm.mMeeting.Hide()
	}
}

func (tm *trayManager) setMeetingRecording() {
	systray.SetTooltip("Tomoe — Meeting Recording...")
	systray.SetIcon(trayIconRecording)
	if tm.mMeeting != nil {
		tm.mMeeting.Hide()
	}
	if tm.mStopMeeting != nil {
		tm.mStopMeeting.Show()
	}
	if tm.mDictation != nil {
		tm.mDictation.Hide()
	}
}
