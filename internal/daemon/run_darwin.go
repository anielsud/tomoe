//go:build darwin

package daemon

import "fyne.io/systray"

// runWithTray calls systray.Run itself, blocking the calling goroutine
// (which must be the process's real main goroutine — daemon.Run() is
// only ever called directly from cmd/tomoe's main(), so this holds).
// Cocoa's status-bar APIs require systray's native loop to run on the
// actual OS main thread; calling systray.Run from a spawned goroutine
// (as startDaemonTray does for Linux) crashes with a low-level AppKit
// assertion failure on darwin. body — everything daemon.Run() actually
// does (hotkey registration, the event select loop, etc.) — runs in its
// own goroutine started from inside onReady instead, and its returned
// error is relayed back out once systray.Run unblocks (which happens as
// a side effect of body's own `defer tray.Close()` calling
// systray.Quit()).
func runWithTray(languages []string, defaultLang string, body func(*daemonTray) error) error {
	tray := newDaemonTray()
	errCh := make(chan error, 1)
	systray.Run(func() {
		tray.onReady(languages, defaultLang)
		go func() {
			errCh <- body(tray)
		}()
	}, func() {})
	return <-errCh
}
