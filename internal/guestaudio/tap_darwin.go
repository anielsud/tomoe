//go:build darwin

// Package guestaudio captures real guest (remote-participant) audio from
// a specific on-screen window via ScreenCaptureKit — the working
// alternative to a CoreAudio Process Tap, which tomoe-darwin's Python
// spike found builds and reports success at every step but never once
// delivers a callback with data (documented dead end, not attempted
// again here; see tomoe-darwin README.md §6.11).
package guestaudio

/*
#cgo LDFLAGS: -framework AppKit -framework ScreenCaptureKit -framework CoreMedia -framework CoreFoundation
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Tap captures guest audio for one active call. One instance per call.
type Tap struct {
	windowID uint32
	system   bool // true: capture the whole system's audio, ignoring windowID
	handle   uintptr
	native   unsafe.Pointer
}

// NewTap creates (but does not start) a Tap for `windowID` (e.g. from
// teamsvideo.FindMeetingWindow or teamsvideo.FindWindowForPID).
// onSamples is called with each batch of real, already-downmixed mono
// float32 samples as they arrive, from an arbitrary ScreenCaptureKit-owned
// thread — keep it fast and non-blocking, same contract as the Python
// original this ports.
func NewTap(windowID uint32, onSamples func(samples []float32, sampleRateHz float64)) *Tap {
	return &Tap{windowID: windowID, handle: registerCallback(onSamples)}
}

// NewSystemTap creates (but does not start) a Tap for the whole
// system's audio output — the source picker's "Everything" option,
// not tied to any one app's window.
func NewSystemTap(onSamples func(samples []float32, sampleRateHz float64)) *Tap {
	return &Tap{system: true, handle: registerCallback(onSamples)}
}

// Start begins capturing. Blocks until ScreenCaptureKit confirms capture
// has actually started (or failed) — same synchronous contract as the
// Python original.
func (t *Tap) Start() error {
	var cErr *C.char
	var native unsafe.Pointer
	if t.system {
		native = C.guestaudio_start_system_tap(C.uintptr_t(t.handle), &cErr)
	} else {
		native = C.guestaudio_start_tap(C.int32_t(t.windowID), C.uintptr_t(t.handle), &cErr)
	}
	if native == nil {
		msg := "unknown error"
		if cErr != nil {
			msg = C.GoString(cErr)
			C.free(unsafe.Pointer(cErr))
		}
		if t.system {
			return fmt.Errorf("guestaudio: failed to start system-wide tap: %s", msg)
		}
		return fmt.Errorf("guestaudio: failed to start tap on window %d: %s", t.windowID, msg)
	}
	t.native = native
	return nil
}

// Stop stops capturing and releases all resources. Safe to call once,
// after a successful Start.
func (t *Tap) Stop() {
	if t.native != nil {
		C.guestaudio_stop_tap(t.native)
		t.native = nil
	}
	unregisterCallback(t.handle)
}
