//go:build linux

package diarize

import "syscall"

// lowerThreadPriority raises the calling OS thread's nice value, so the
// scheduler favors interactive work (the call, the live transcript). The
// caller must have locked its goroutine to the thread.
func lowerThreadPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 10)
}
