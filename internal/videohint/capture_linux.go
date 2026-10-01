//go:build linux

package videohint

// No screen-based hints on Linux: Watcher.Run just waits.
const captureSupported = false

func captureMeetingWindow() (*frame, EventStage, string) {
	return nil, StageWindowNotFound, "video hints aren't supported on Linux"
}
