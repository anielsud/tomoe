//go:build darwin

package backend

/*
#include <dispatch/dispatch.h>

extern void goRunOnMainThreadCallback(unsigned long handle);

// dispatch_to_main schedules the Go callback identified by handle to
// run on the Cocoa main thread via GCD's main queue. This works
// regardless of which thread calls it and regardless of whether
// [NSApp run] (Wails' Window.Run, in this process) has started
// pumping the run loop yet -- GCD just queues the block until the main
// run loop is live enough to process it.
static void dispatch_to_main(unsigned long handle) {
	dispatch_async(dispatch_get_main_queue(), ^{
		goRunOnMainThreadCallback(handle);
	});
}
*/
import "C"

import "sync"

var (
	mainThreadCallbacksMu sync.Mutex
	mainThreadCallbacks   = map[uint64]func(){}
	mainThreadNextHandle  uint64
)

// runOnMainThread schedules fn to run on the real Cocoa main thread,
// regardless of which goroutine calls it. Needed for
// systray.RunWithExternalLoop's start/end functions — see
// tray_start_darwin.go for why.
func runOnMainThread(fn func()) {
	mainThreadCallbacksMu.Lock()
	mainThreadNextHandle++
	h := mainThreadNextHandle
	mainThreadCallbacks[h] = fn
	mainThreadCallbacksMu.Unlock()

	C.dispatch_to_main(C.ulong(h))
}

//export goRunOnMainThreadCallback
func goRunOnMainThreadCallback(handle C.ulong) {
	mainThreadCallbacksMu.Lock()
	fn := mainThreadCallbacks[uint64(handle)]
	delete(mainThreadCallbacks, uint64(handle))
	mainThreadCallbacksMu.Unlock()

	if fn != nil {
		fn()
	}
}
