// Package audiosources lists which running applications are currently
// producing audio output, so a "System Audio" picker can show real,
// live options instead of a static "Auto-detect" label that either
// works or silently doesn't. Only meaningful on macOS today (see
// audiosources_darwin.go) — Linux already has its own device-name-based
// monitor source listing in internal/audio, which this package doesn't
// touch or replace.
package audiosources

// Source is one currently-active audio-producing process.
type Source struct {
	// PID identifies the source for CaptureSource/StartSession — stable
	// for the lifetime of the process, which is all that's needed here
	// (a picker showing currently-running apps, not a persistent ID).
	PID int
	// Name is a human-readable app name (from NSRunningApplication),
	// falling back to BundleID or "PID <n>" if unavailable.
	Name string
	// BundleID is the app's bundle identifier (e.g. "com.microsoft.teams2"),
	// used to recognize known meeting apps -- see internal/meeting's
	// darwin detector. Empty for processes with no real app bundle
	// (rare for anything with a UI, common for bare CLI tools).
	BundleID string
}
