//go:build darwin

package meetingaudio

import (
	"fmt"
	"strconv"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/guestaudio"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// NewMonitorSource captures system audio via ScreenCaptureKit, per
// sourceHint (from the frontend's source picker — see
// internal/audiosources.ListActive and internal/backend's
// ListAudioSources/StartSession):
//   - "" (nothing selected): no monitor capture, mic-only.
//   - "everything": the whole system's audio output, not tied to any
//     one app (internal/guestaudio.NewSystemCapturer).
//   - a decimal PID (e.g. "1234", from ListActive): that specific
//     app's audio, via a window it owns (teamsvideo.FindWindowForPID +
//     internal/guestaudio.NewWindowCapturer).
//
// Unlike the Linux implementation, failure here is never fatal: no
// window found, a bad/stale PID, and a Tap failing to start (most
// commonly: Screen Recording permission — System Settings > Privacy &
// Security > Screen Recording — hasn't been granted) all just fall
// back to mic-only, which is still a fully-working session. A hard
// failure here would make meeting mode strictly worse than today for
// no benefit.
func NewMonitorSource(sourceHint string) (*audio.StreamCapturer, error) {
	if sourceHint == "" {
		return nil, nil
	}

	var capturer audio.Capturer

	if sourceHint == "everything" {
		capturer = guestaudio.NewSystemCapturer()
	} else {
		pid, err := strconv.ParseInt(sourceHint, 10, 32)
		if err != nil {
			fmt.Printf("meetingaudio: invalid source selection %q, starting mic-only: %v\n", sourceHint, err)
			return nil, nil
		}
		windowID, err := teamsvideo.FindWindowForPID(int32(pid))
		if err != nil {
			fmt.Printf("meetingaudio: no window found for selected app (PID %d), starting mic-only: %v\n", pid, err)
			return nil, nil
		}
		capturer = guestaudio.NewWindowCapturer(uint32(windowID))
	}

	// Start now, up front, to confirm the tap actually works (most
	// commonly: Screen Recording permission not yet granted) before
	// committing to it as the monitor source. Capturer.Start is
	// idempotent, so the StreamCapturer below calling Start again when
	// the coordinator starts the session is a no-op -- real capture,
	// started here, just keeps running.
	if err := capturer.Start(); err != nil {
		fmt.Printf("meetingaudio: guest audio capture unavailable, starting mic-only (%v) — check Screen Recording permission in System Settings > Privacy & Security\n", err)
		return nil, nil
	}

	return audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128), nil
}
