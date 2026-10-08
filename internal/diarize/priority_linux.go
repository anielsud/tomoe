//go:build linux

package diarize

import "syscall"

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
