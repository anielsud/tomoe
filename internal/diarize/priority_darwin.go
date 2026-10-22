//go:build darwin

package diarize

/*
#cgo LDFLAGS: -lobjc
#include <pthread.h>
#include <pthread/qos.h>

extern void *objc_autoreleasePoolPush(void);
extern void objc_autoreleasePoolPop(void *);

static void *poolPush(void) { return objc_autoreleasePoolPush(); }
static void poolPop(void *p) { objc_autoreleasePoolPop(p); }

static int defaultQoS(void) {
	return pthread_set_qos_class_self_np(QOS_CLASS_DEFAULT, 0);
}

static int lowerQoS(void) {
	return pthread_set_qos_class_self_np(QOS_CLASS_UTILITY, 0);
}

static int raiseQoS(void) {
	return pthread_set_qos_class_self_np(QOS_CLASS_USER_INITIATED, 0);
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// lowerThreadPriority marks the calling OS thread as utility work, which
// macOS schedules behind interactive work (the call, the live transcript)
// and runs on efficiency cores where it can. The caller must have locked
// its goroutine to the thread.
func lowerThreadPriority() {
	_ = C.lowerQoS()
}

// raiseThreadPriority undoes lowerThreadPriority once nothing interactive
// is left to make room for (the meeting has ended and the user is waiting
// for its final labels): user-initiated work, eligible for performance
// cores.
func raiseThreadPriority() {
	_ = C.raiseQoS()
}

// autoreleasePush opens an Objective-C autorelease pool on the calling
// thread, for the model calls that follow (ONNX Runtime autoreleases on
// macOS); autoreleasePop drains it while what it holds is still alive.
// Left to the thread's exit, the leftovers of a meeting's worth of windows
// were released after the models were gone, and the exiting thread spun
// at 100% CPU for good: one more such thread after every meeting.
func autoreleasePush() unsafe.Pointer { return C.poolPush() }

func autoreleasePop(p unsafe.Pointer) { C.poolPop(p) }

// releaseThread gives the Stream's OS thread back to Go at normal
// priority instead of letting it exit with the goroutine (see
// autoreleasePush).
func releaseThread() {
	_ = C.defaultQoS()
	runtime.UnlockOSThread()
}
