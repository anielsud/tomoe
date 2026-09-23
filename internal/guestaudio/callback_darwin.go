//go:build darwin

package guestaudio

/*
#include "bridge.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

var (
	callbacksMu sync.Mutex
	callbacks   = map[uintptr]func(samples []float32, sampleRateHz float64){}
	nextHandle  uintptr
)

func registerCallback(fn func(samples []float32, sampleRateHz float64)) uintptr {
	callbacksMu.Lock()
	defer callbacksMu.Unlock()
	nextHandle++
	h := nextHandle
	callbacks[h] = fn
	return h
}

func unregisterCallback(h uintptr) {
	callbacksMu.Lock()
	defer callbacksMu.Unlock()
	delete(callbacks, h)
}

//export goGuestAudioOnSamples
func goGuestAudioOnSamples(handle C.uintptr_t, samples *C.float, n C.int, sampleRate C.double) {
	callbacksMu.Lock()
	fn := callbacks[uintptr(handle)]
	callbacksMu.Unlock()
	if fn == nil || n <= 0 {
		return
	}

	// `samples` aliases memory the Objective-C caller frees immediately
	// after this function returns, so raw must be fully processed/copied
	// before we return -- it is never retained past this call.
	raw := unsafe.Slice((*float32)(unsafe.Pointer(samples)), int(n))

	// ScreenCaptureKit delivers planar (non-interleaved) stereo float32,
	// not interleaved -- confirmed empirically 2026-09-23 by correlating a
	// captured buffer's first half against its second half (corr=1.0000,
	// zero variance, across 250 real buffers) vs. treating it as
	// interleaved (corr~0.95, just real speech's own adjacent-sample
	// autocorrelation, not two independent channels). See tomoe-darwin
	// README.md §11 and demo/live_demo.py's on_samples fix -- getting this
	// wrong there silently scrambled every guest transcription without
	// raising any error. Applied correctly here from the start.
	half := len(raw) / 2
	mono := make([]float32, half)
	for i := 0; i < half; i++ {
		mono[i] = (raw[i] + raw[half+i]) / 2
	}
	fn(mono, float64(sampleRate))
}
