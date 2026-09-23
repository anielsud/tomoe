//go:build darwin

package meetingaudio

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/guestaudio"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// NewMonitorSource ignores deviceHint (there's no PulseAudio-shaped
// device concept on macOS) and instead looks for the active Teams
// meeting window, capturing its audio via ScreenCaptureKit if found.
//
// Unlike the Linux implementation, failure here is never fatal: no
// window found (not currently in a call, or a meeting app teamsvideo
// doesn't recognize yet) and a Tap failing to start (most likely because
// Screen Recording permission — System Settings > Privacy & Security >
// Screen Recording — hasn't been granted) both just fall back to
// mic-only, which is still a fully-working session. A hard failure here
// would make meeting mode strictly worse than today for no benefit.
func NewMonitorSource(_ string) (*audio.StreamCapturer, error) {
	windowID, err := teamsvideo.FindMeetingWindow()
	if err != nil {
		fmt.Printf("meetingaudio: no meeting window found, starting mic-only (%v)\n", err)
		return nil, nil
	}

	capturer := guestaudio.NewWindowCapturer(uint32(windowID))
	// Start now, up front, to confirm the tap actually works (most
	// commonly: Screen Recording permission not yet granted) before
	// committing to it as the monitor source. WindowCapturer.Start is
	// idempotent, so the StreamCapturer below calling Start again when
	// the coordinator starts the session is a no-op -- real capture,
	// started here, just keeps running.
	if err := capturer.Start(); err != nil {
		fmt.Printf("meetingaudio: guest audio capture unavailable, starting mic-only (%v) — check Screen Recording permission in System Settings > Privacy & Security\n", err)
		return nil, nil
	}

	return audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128), nil
}
