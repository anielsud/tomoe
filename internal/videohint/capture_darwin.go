//go:build darwin

package videohint

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

const captureSupported = true

// captureMeetingWindow finds the Teams meeting window and captures it.
// The frame is nil when there's nothing to look at, with the stage saying
// why.
func captureMeetingWindow() (*frame, EventStage, string) {
	id, err := teamsvideo.FindMeetingWindow()
	if err != nil {
		return nil, StageWindowNotFound, "no Teams meeting window on screen"
	}
	f, err := teamsvideo.CaptureWindowRGB(id)
	if err != nil {
		return nil, StageCaptureFailed, err.Error()
	}
	return &frame{width: f.Width, height: f.Height, pix: f.Pix}, StageFrameCaptured, fmt.Sprintf("captured %dx%d", f.Width, f.Height)
}
