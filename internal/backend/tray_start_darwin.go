//go:build darwin

package backend

import "fyne.io/systray"

// trayEnd is the teardown closure RunWithExternalLoop hands back.
// Package-level because App.Shutdown (in app.go, shared with Linux)
// calls the OS-agnostic StopTray() rather than reaching into App itself.
var trayEnd func()

// StartTrayAsync registers the tray using systray's external-loop mode
// (systray.RunWithExternalLoop) instead of systray.Run, because on
// darwin, Wails' own window already owns the real Cocoa main thread and
// calls [NSApp run] itself (inside Window.Run, blocking cmd/tomoe-gui's
// actual main goroutine) — there is no second main run loop for systray
// to take over the way run_darwin.go lets it for the CLI daemon
// (internal/daemon), which has no competing window/run loop at all.
//
// registerSystray's darwin implementation checks exactly this: in
// external-loop mode it does NOT call [NSApp run] and does NOT replace
// NSApplication's delegate (so it can't clobber Wails' own delegate
// either) — RunWithExternalLoop's start() just directly builds the
// status item/menu once, synchronously, and end() tears it down. Both
// still need to happen on the real main thread (AppKit's status-item
// APIs assume it), so they're dispatched there via runOnMainThread
// (dispatch_darwin.go) rather than called from whatever goroutine
// App.Startup/Shutdown happens to run on — which the earlier crash here
// proved is *not* the main thread. Ordinary menu interaction (onReady,
// item clicks) doesn't need this: systray's own ObjC side already
// dispatches those via performSelectorOnMainThread internally.
func StartTrayAsync(app *App) {
	start, end := systray.RunWithExternalLoop(func() { onTrayReady(app) }, func() {})
	trayEnd = end
	runOnMainThread(start)
}

// StopTray tears down the tray. Called from App.Shutdown.
func StopTray() {
	if trayEnd != nil {
		runOnMainThread(trayEnd)
	}
}
