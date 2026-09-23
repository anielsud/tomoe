//go:build darwin

package teamsvideo

/*
#cgo CFLAGS: -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    unsigned char *data;     // malloc'd raw pixel bytes; caller must free. NULL on failure.
    size_t width;
    size_t height;
    size_t bytes_per_row;    // may exceed width*4 (row padding) -- Go must respect this stride.
} capture_result_t;

// Captures `window_id` right now and copies out its raw pixel buffer
// (BGRA, per prior live-confirmed testing in the Python spike this ports --
// tomoe-darwin's teams_video_signal/capture.py). No temp files, no
// subprocess: CGWindowListCreateImage reads the window-server's compositor
// buffer directly, which is what makes this survive on-screen occlusion and
// a minimized window state (tomoe-darwin README.md §6.9).
static capture_result_t capture_window(int32_t window_id) {
    capture_result_t result;
    memset(&result, 0, sizeof(result));

    CGImageRef image = CGWindowListCreateImage(
        CGRectNull,
        kCGWindowListOptionIncludingWindow,
        (CGWindowID)window_id,
        kCGWindowImageBoundsIgnoreFraming);
    if (image == NULL) {
        return result;
    }

    size_t width = CGImageGetWidth(image);
    size_t height = CGImageGetHeight(image);
    size_t bytes_per_row = CGImageGetBytesPerRow(image);
    CGDataProviderRef provider = CGImageGetDataProvider(image);
    CFDataRef raw = CGDataProviderCopyData(provider);
    if (raw == NULL) {
        CGImageRelease(image);
        return result;
    }

    size_t total = bytes_per_row * height;
    unsigned char *buf = malloc(total);
    if (buf != NULL) {
        memcpy(buf, CFDataGetBytePtr(raw), total);
        result.data = buf;
        result.width = width;
        result.height = height;
        result.bytes_per_row = bytes_per_row;
    }

    CFRelease(raw);
    CGImageRelease(image);
    return result;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Frame is a captured window's pixels as packed 8-bit RGB (no alpha),
// row-major, no padding — len(Pix) == Width*Height*3.
type Frame struct {
	Width  int
	Height int
	Pix    []byte
}

// CaptureWindowRGB captures `id` right now, in memory — no temp files, no
// subprocess — so this is cheap enough to call in a live polling loop
// (confirmed live in the Python spike at ~1s intervals).
func CaptureWindowRGB(id WindowID) (*Frame, error) {
	res := C.capture_window(C.int32_t(id))
	if res.data == nil {
		return nil, fmt.Errorf("teamsvideo: failed to capture window %d (closed? off-screen?)", id)
	}
	defer C.free(unsafe.Pointer(res.data))

	width := int(res.width)
	height := int(res.height)
	stride := int(res.bytes_per_row)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(res.data)), stride*height)

	// BGRA -> RGB (confirmed byte order in the Python spike this ports;
	// same window-server pixel format either way we get there), and
	// collapse the row stride down to a tight width*3 packing.
	pix := make([]byte, width*height*3)
	for y := 0; y < height; y++ {
		row := raw[y*stride : y*stride+width*4]
		out := pix[y*width*3 : (y+1)*width*3]
		for x := 0; x < width; x++ {
			b, g, r := row[x*4], row[x*4+1], row[x*4+2]
			out[x*3], out[x*3+1], out[x*3+2] = r, g, b
		}
	}

	return &Frame{Width: width, Height: height, Pix: pix}, nil
}
