package backend

import "fyne.io/systray"

// StartTrayAsync starts the system tray in a goroutine. Linux's
// AppIndicator3-backed systray has no main-thread affinity requirement,
// unlike darwin's Cocoa-backed one (see tray_start_darwin.go).
func StartTrayAsync(app *App) {
	go systray.Run(func() { onTrayReady(app) }, func() {})
}

// StopTray is a no-op on Linux: the quit menu item's own handler in
// onTrayReady already calls systray.Quit() directly, and there is no
// separate external-loop teardown step to run (that's a darwin-only
// concern — see tray_start_darwin.go).
func StopTray() {}
