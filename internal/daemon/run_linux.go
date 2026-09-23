package daemon

// runWithTray starts the tray (in its own goroutine, as startDaemonTray
// always has) and calls body immediately — Linux's AppIndicator3-backed
// systray has no main-thread affinity requirement, unlike darwin's
// Cocoa-backed one. See run_darwin.go for the platform that does.
func runWithTray(languages []string, defaultLang string, body func(*daemonTray) error) error {
	tray := startDaemonTray(languages, defaultLang)
	return body(tray)
}
