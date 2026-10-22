//go:build !darwin && !linux

package diarize

import "unsafe"

func lowerThreadPriority() {}

func raiseThreadPriority() {}

// autoreleasePush and autoreleasePop are macOS's (Objective-C pools).
func autoreleasePush() unsafe.Pointer { return nil }

func autoreleasePop(unsafe.Pointer) {}

// releaseThread does nothing: the Stream's thread keeps its lowered
// priority (it can't be raised back), so it ends with its goroutine.
func releaseThread() {}
