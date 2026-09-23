//go:build darwin

package meeting

import (
	"context"
	"fmt"
	"syscall"
)

// Automatic meeting detection has no macOS implementation yet. Linux's
// signal (simultaneous PulseAudio source-output + sink-input from one
// PID) has no direct macOS equivalent — see docs/macos-support.md. This
// file exists only to satisfy detect.go's build-time dependency on the
// PulseAudio-shaped helpers below, so internal/meeting (and everything
// that imports it: cmd/tomoe, internal/daemon, internal/backend)
// compiles on darwin. pulseInit always fails, so Detector.Start returns
// an error immediately and none of the other functions below ever run
// — every call site already treats a Start() error as "feature
// disabled, continue" (see internal/daemon/daemon.go and
// internal/backend/app.go), so this is safe.
//
// Real macOS meeting detection is a follow-up (phase 2, alongside
// internal/teamsvideo OCR and internal/guestaudio wiring).

func pulseInit() error {
	return fmt.Errorf("automatic meeting detection is not yet implemented on macOS")
}

func pulseSubscribe() error {
	return fmt.Errorf("automatic meeting detection is not yet implemented on macOS")
}

func pulseCleanup() {}

func pulseQuit() {}

func pulseEventLoop(_ context.Context) {}

func pulseListSinkInputs() []streamInfo {
	return nil
}

func pulseListSourceOutputs() []streamInfo {
	return nil
}

func setActiveDetector(_ *Detector) {}

// getWindowTitleByPID has no macOS implementation yet (no xdotool
// equivalent wired up) — browser-based meeting platform identification
// via identifyPlatform (in platform.go) always falls through to
// PlatformUnknown for browser apps on darwin until this is implemented.
func getWindowTitleByPID(_ int) string {
	return ""
}

// processExists checks if a process with the given PID is still
// running, via a signal-0 liveness probe (the macOS equivalent of
// Linux's /proc/<pid> check, which has no macOS counterpart).
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// PulseAudio doesn't exist on macOS. These constants and comparison
// functions are never fed real events (pulseInit always fails above,
// so Detector.Start never gets far enough to call them) — they exist,
// self-consistent and with the same shape as pulse_linux.go's real
// PulseAudio-derived ones, only so this package and the shared
// detect_test.go compile and pass identically on both platforms.
const (
	paFacilitySinkInput    = 0
	paFacilitySourceOutput = 1
	paEventNew             = 0
	paEventRemove          = 1
)

func isSourceOutputNew(facility, eventType int) bool {
	return facility == paFacilitySourceOutput && eventType == paEventNew
}

func isSourceOutputRemove(facility, eventType int) bool {
	return facility == paFacilitySourceOutput && eventType == paEventRemove
}

func isSinkInputNew(facility, eventType int) bool {
	return facility == paFacilitySinkInput && eventType == paEventNew
}

func isSinkInputRemove(facility, eventType int) bool {
	return facility == paFacilitySinkInput && eventType == paEventRemove
}
