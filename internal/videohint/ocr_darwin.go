//go:build darwin

package videohint

/*
#cgo LDFLAGS: -framework Vision -framework CoreGraphics -framework Foundation
#include <stdlib.h>
#include "ocr_bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// RecognizeText runs Vision.framework OCR against a packed RGB (no
// alpha, no row padding) pixel buffer — the same shape
// teamsvideo.Frame.Pix and DetectRing's pix parameter use. Returns the
// recognized text (possibly empty if nothing was found), or an error.
func RecognizeText(pix []byte, width, height int) (string, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return "", fmt.Errorf("videohint: pixel buffer too small for %dx%d RGB", width, height)
	}

	var cErr *C.char
	result := C.videohint_recognize_text((*C.uint8_t)(unsafe.Pointer(&pix[0])), C.int32_t(width), C.int32_t(height), &cErr)
	if result == nil {
		msg := "unknown error"
		if cErr != nil {
			msg = C.GoString(cErr)
			C.free(unsafe.Pointer(cErr))
		}
		return "", fmt.Errorf("videohint: OCR failed: %s", msg)
	}
	defer C.free(unsafe.Pointer(result))
	return C.GoString(result), nil
}
