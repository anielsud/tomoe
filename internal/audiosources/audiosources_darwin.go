//go:build darwin

package audiosources

/*
#cgo LDFLAGS: -framework CoreAudio -framework AudioToolbox -framework AppKit -framework Foundation
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// ListActive returns every process currently producing audio output,
// via CoreAudio's process-object properties (kAudioHardwarePropertyProcessObjectList
// + kAudioProcessPropertyIsRunningOutput) — confirmed live against a
// real process starting/stopping audio, not just documented behavior.
// This is a different CoreAudio code path from the Process Tap *capture*
// API internal/guestaudio's own doc comment already found undeliverable
// on this codebase's target macOS versions (that's about streaming
// audio *data* via a callback; this is a synchronous property query,
// with no callback involved) — the two aren't in tension.
func ListActive() ([]Source, error) {
	var cSources *C.audiosources_source_t
	count := C.audiosources_list_active(&cSources)
	if count < 0 {
		return nil, fmt.Errorf("audiosources: failed to query active audio processes")
	}
	if count == 0 {
		return nil, nil
	}
	defer C.audiosources_free(cSources, count)

	slice := unsafe.Slice(cSources, int(count))
	out := make([]Source, 0, count)
	for _, s := range slice {
		src := Source{PID: int(s.pid)}
		if s.name != nil {
			src.Name = C.GoString(s.name)
		}
		if s.bundle_id != nil {
			src.BundleID = C.GoString(s.bundle_id)
		}
		if src.Name == "" {
			if src.BundleID != "" {
				src.Name = src.BundleID
			} else {
				src.Name = fmt.Sprintf("PID %d", src.PID)
			}
		}
		out = append(out, src)
	}
	return out, nil
}
