package meetingaudio

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
)

// NewMonitorSource resolves deviceHint (falling back to
// audio.DefaultMonitorDevice() if empty) and wraps it as a
// StreamCapturer, or returns (nil, nil) if no monitor device is
// available — unchanged from what internal/daemon and internal/backend
// did inline before this package existed.
func NewMonitorSource(deviceHint string) (*audio.StreamCapturer, error) {
	device := deviceHint
	if device == "" {
		device = audio.DefaultMonitorDevice()
	}
	if device == "" {
		return nil, nil
	}

	capturer, err := audio.NewCapturer(device, audio.Monitor)
	if err != nil {
		return nil, fmt.Errorf("creating monitor capturer: %w", err)
	}
	return audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128), nil
}
