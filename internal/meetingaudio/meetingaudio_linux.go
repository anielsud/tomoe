package meetingaudio

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
)

// NewMonitorSource resolves deviceHint (falling back to
// audio.DefaultMonitorDevice() if empty) and wraps it as a
// StreamCapturer, or returns (nil, nil) if deviceHint is NoSource or no
// monitor device is available.
func NewMonitorSource(deviceHint string) (*audio.StreamCapturer, error) {
	if deviceHint == NoSource {
		return nil, nil
	}
	device := deviceHint
	if device == "" || device == AutoSource {
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

// Auto is a macOS AutoSource capture; Linux has none.
type Auto struct{}

// Current is "" on Linux.
func (a *Auto) Current() string { return "" }

// TryMeetingApp does nothing on Linux.
func (a *Auto) TryMeetingApp() (string, bool) { return "", false }

// NewAutoMonitorSource is the default monitor source on Linux.
func NewAutoMonitorSource() (*audio.StreamCapturer, *Auto, error) {
	s, err := NewMonitorSource("")
	return s, nil, err
}
