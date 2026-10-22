//go:build linux

package diarize

import (
	"syscall"
	"unsafe"
)

// lowerThreadPriority raises the calling OS thread's nice value, so the
// scheduler favors interactive work (the call, the live transcript). The
// caller must have locked its goroutine to the thread.
func lowerThreadPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 10)
}

// raiseThreadPriority would undo lowerThreadPriority, but lowering a nice
// value back needs privileges a desktop app doesn't have, so the thread
// stays as it is.
func raiseThreadPriority() {}

// autoreleasePush and autoreleasePop are macOS's (Objective-C pools).
func autoreleasePush() unsafe.Pointer { return nil }

func autoreleasePop(unsafe.Pointer) {}

// releaseThread does nothing: the Stream's thread keeps its lowered
// priority (it can't be raised back), so it ends with its goroutine.
func releaseThread() {}
