#ifndef GUESTAUDIO_BRIDGE_H
#define GUESTAUDIO_BRIDGE_H

#include <stdint.h>

// Finds `window_id` via SCShareableContent, builds an SCContentFilter +
// SCStreamConfiguration (audio, excluding this process's own audio) +
// SCStream, registers an SCStreamOutput handler, and starts capture --
// synchronously (blocks until ScreenCaptureKit's own async completion
// handlers report back, mirroring tomoe-darwin's Python spike's
// threading.Event().wait() pattern for the same async APIs).
//
// Establishes the window-server connection SCStream needs as a side
// effect of the first call (mirrors the fix for the "Assertion failed:
// (did_initialize), function CGS_REQUIRE_INIT" crash found live in
// tomoe-darwin's Python spike -- a real macOS requirement independent of
// the calling language, not a Python/pyobjc quirk).
//
// `go_handle` is an opaque token Go uses to route callbacks back to the
// right Tap instance; it is passed back unchanged to
// goGuestAudioOnSamples.
//
// Returns an opaque tap handle (a retained SCStream) on success, or NULL
// on failure -- in which case *out_error is set to a malloc'd C string
// the caller must free.
void *guestaudio_start_tap(int32_t window_id, uintptr_t go_handle, char **out_error);

// Same as guestaudio_start_tap, but captures the whole system's audio
// output (the first display SCShareableContent reports) instead of one
// window's -- for the source picker's "Everything" option.
void *guestaudio_start_system_tap(uintptr_t go_handle, char **out_error);

// Stops capture and releases the tap. Safe to call exactly once per
// successful guestaudio_start_tap/guestaudio_start_system_tap call.
void guestaudio_stop_tap(void *tap);

#endif
