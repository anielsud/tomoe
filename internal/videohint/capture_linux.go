//go:build linux

package videohint

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// No screen-based hints on Linux: Watcher.Run just waits.
const captureSupported = false

func captureWindow(string) (*frame, meeting.Platform, string, EventStage, string) {
	return nil, "", "", StageWindowNotFound, "video hints aren't supported on Linux"
}

// Windows lists nothing on Linux.
func Windows() ([]WindowChoice, error) { return nil, nil }

// No window inventory on Linux.
func snapshotWindows() []teamsvideo.WindowRecord { return nil }

func captureWindowByID(int) (*frame, error) {
	return nil, fmt.Errorf("window capture isn't supported on Linux")
}
