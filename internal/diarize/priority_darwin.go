//go:build darwin

package diarize

/*
#include <pthread.h>
#include <pthread/qos.h>

static int lowerQoS(void) {
	return pthread_set_qos_class_self_np(QOS_CLASS_UTILITY, 0);
}
*/
import "C"

// lowerThreadPriority marks the calling OS thread as utility work, which
// macOS schedules behind interactive work (the call, the live transcript)
// and runs on efficiency cores where it can. The caller must have locked
// its goroutine to the thread.
func lowerThreadPriority() {
	_ = C.lowerQoS()
}
