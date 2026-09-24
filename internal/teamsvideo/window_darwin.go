//go:build darwin

// Package teamsvideo captures the active Microsoft Teams meeting window on
// macOS and exposes it as a plain RGB frame, so the (pure Go, no cgo)
// ring-detection and future OCR code downstream can operate on it without
// any platform dependency of their own.
//
// Ported from tomoe-darwin's validated Python/pyobjc spike
// (teams_video_signal/capture.py, tomoe-darwin README.md §6.9-§6.10) — this
// file and capture_darwin.go are the cgo translation of that same,
// already-live-confirmed mechanism (CGWindowListCopyWindowInfo /
// CGWindowListCreateImage), not a new design. The C side here only makes
// the raw CoreGraphics calls and hands typed values back to Go; the actual
// filtering/matching logic lives in Go so it's easy to read, test, and keep
// in sync with the Python original it's replacing.
package teamsvideo

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdint.h>
#include <stdlib.h>

// Copies a CFString value for `key` out of `dict` as a malloc'd UTF-8 C
// string (caller must free), or NULL if the key is absent or not a string.
static char *dict_get_string(CFDictionaryRef dict, CFStringRef key) {
    CFStringRef val = (CFStringRef)CFDictionaryGetValue(dict, key);
    if (val == NULL || CFGetTypeID(val) != CFStringGetTypeID()) {
        return NULL;
    }
    CFIndex len = CFStringGetMaximumSizeForEncoding(CFStringGetLength(val), kCFStringEncodingUTF8) + 1;
    char *buf = malloc((size_t)len);
    if (buf == NULL) {
        return NULL;
    }
    if (!CFStringGetCString(val, buf, len, kCFStringEncodingUTF8)) {
        free(buf);
        return NULL;
    }
    return buf;
}

// Returns the int32 value for `key` in `dict`, or -1 if absent/not a number.
static int32_t dict_get_int(CFDictionaryRef dict, CFStringRef key) {
    CFNumberRef val = (CFNumberRef)CFDictionaryGetValue(dict, key);
    if (val == NULL || CFGetTypeID(val) != CFNumberGetTypeID()) {
        return -1;
    }
    int32_t out = 0;
    CFNumberGetValue(val, kCFNumberSInt32Type, &out);
    return out;
}

static CFArrayRef list_windows(void) {
    return CGWindowListCopyWindowInfo(
        kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
        kCGNullWindowID);
}

// cfarray_is_null does the NULL check in C rather than Go: cgo's
// CFArrayRef doesn't support a direct `== nil` comparison on the Go
// side (mismatched types), and converting through unsafe.Pointer just
// to compare against nil is exactly the pattern `go vet`'s unsafeptr
// check flags as a possible misuse. This sidesteps both.
static int cfarray_is_null(CFArrayRef arr) {
    return arr == NULL;
}

static CFIndex window_count(CFArrayRef windows) {
    return CFArrayGetCount(windows);
}

static char *window_owner_name(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    return dict_get_string(w, kCGWindowOwnerName);
}

static char *window_name(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    return dict_get_string(w, kCGWindowName);
}

static int32_t window_number(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    return dict_get_int(w, kCGWindowNumber);
}

static int32_t window_owner_pid(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    return dict_get_int(w, kCGWindowOwnerPID);
}

static int32_t window_layer(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    return dict_get_int(w, kCGWindowLayer);
}

// kCGWindowBounds is itself a nested dict (a CGRect's dictionary
// representation), not a plain number -- these two need
// CGRectMakeWithDictionaryRepresentation rather than dict_get_int.
static double window_bounds_width(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    CFDictionaryRef bounds = (CFDictionaryRef)CFDictionaryGetValue(w, kCGWindowBounds);
    if (bounds == NULL) {
        return 0;
    }
    CGRect rect;
    if (!CGRectMakeWithDictionaryRepresentation(bounds, &rect)) {
        return 0;
    }
    return rect.size.width;
}

static double window_bounds_height(CFArrayRef windows, CFIndex i) {
    CFDictionaryRef w = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
    CFDictionaryRef bounds = (CFDictionaryRef)CFDictionaryGetValue(w, kCGWindowBounds);
    if (bounds == NULL) {
        return 0;
    }
    CGRect rect;
    if (!CGRectMakeWithDictionaryRepresentation(bounds, &rect)) {
        return 0;
    }
    return rect.size.height;
}

static void release_windows(CFArrayRef windows) {
    CFRelease(windows);
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// WindowID identifies a captured on-screen window (a CGWindowID).
type WindowID uint32

// FindMeetingWindow returns the CGWindowID of the active Microsoft Teams
// meeting window, or an error if none is currently on screen.
//
// Heuristic (unchanged from the Python spike this ports): a "Microsoft
// Teams"-owned window whose title does not start with "Chat |" (the chat
// side panel is its own separate window) and isn't empty/"Window"
// (menu-bar-ish artifacts).
func FindMeetingWindow() (WindowID, error) {
	windows := C.list_windows()
	if C.cfarray_is_null(windows) != 0 {
		return 0, fmt.Errorf("teamsvideo: CGWindowListCopyWindowInfo returned nil")
	}
	defer C.release_windows(windows)

	n := int(C.window_count(windows))
	for i := 0; i < n; i++ {
		idx := C.CFIndex(i)

		ownerPtr := C.window_owner_name(windows, idx)
		if ownerPtr == nil {
			continue
		}
		owner := C.GoString(ownerPtr)
		C.free(unsafe.Pointer(ownerPtr))

		if !strings.Contains(strings.ToLower(owner), "teams") {
			continue
		}

		namePtr := C.window_name(windows, idx)
		title := ""
		if namePtr != nil {
			title = C.GoString(namePtr)
			C.free(unsafe.Pointer(namePtr))
		}
		if title == "" || strings.HasPrefix(title, "Chat |") || title == "Window" {
			continue
		}

		num := int32(C.window_number(windows, idx))
		if num < 0 {
			continue
		}
		return WindowID(num), nil
	}
	return 0, fmt.Errorf("teamsvideo: no Microsoft Teams meeting window found")
}

// FindWindowForPID returns the CGWindowID of the largest on-screen,
// normal-layer (kCGWindowLayer == 0 — excludes menu-bar items, the
// Dock, and other non-content overlays) window owned by pid, for
// tapping a specific app's audio when the user picks it from
// internal/audiosources.ListActive rather than relying on
// FindMeetingWindow's Teams-specific title heuristic.
func FindWindowForPID(pid int32) (WindowID, error) {
	windows := C.list_windows()
	if C.cfarray_is_null(windows) != 0 {
		return 0, fmt.Errorf("teamsvideo: CGWindowListCopyWindowInfo returned nil")
	}
	defer C.release_windows(windows)

	n := int(C.window_count(windows))
	var best WindowID
	var bestArea int64 = -1

	for i := 0; i < n; i++ {
		idx := C.CFIndex(i)

		if int32(C.window_owner_pid(windows, idx)) != pid {
			continue
		}
		if int32(C.window_layer(windows, idx)) != 0 {
			continue
		}

		num := int32(C.window_number(windows, idx))
		if num < 0 {
			continue
		}

		w := int64(C.window_bounds_width(windows, idx))
		h := int64(C.window_bounds_height(windows, idx))
		area := w * h
		if area > bestArea {
			bestArea = area
			best = WindowID(num)
		}
	}

	if bestArea < 0 {
		return 0, fmt.Errorf("teamsvideo: no on-screen window found for PID %d", pid)
	}
	return best, nil
}
