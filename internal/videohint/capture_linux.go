//go:build linux

package videohint

import "github.com/sosuke-ai/tomoe-pc/internal/meeting"

// No screen-based hints on Linux: Watcher.Run just waits.
const captureSupported = false

func captureWindow(string) (*frame, meeting.Platform, string, EventStage, string) {
	return nil, "", "", StageWindowNotFound, "video hints aren't supported on Linux"
}

// Windows lists nothing on Linux.
func Windows() ([]WindowChoice, error) { return nil, nil }
