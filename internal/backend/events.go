package backend

import (
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// emitSegments reads segments from the coordinator and emits them to the frontend.
// Must be called as a goroutine. Runs until the coordinator's segment channel is closed.
func (a *App) emitSegments() {
	if a.coordinator == nil {
		return
	}

	for seg := range a.coordinator.Segments() {
		// Append to current session
		a.mu.Lock()
		if a.currentSess != nil {
			a.currentSess.Segments = append(a.currentSess.Segments, seg)
		}
		a.mu.Unlock()

		// Emit to frontend
		wailsRuntime.EventsEmit(a.ctx, "transcript:segment", seg)
	}
}

// emitSegmentUpdates reads pass-2 refinements from the coordinator
// (same segment ID as something emitSegments already sent, superseding
// text) and emits them to the frontend as a distinct event, since the
// frontend needs to update a line in place rather than append a new
// one. Must be called as a goroutine. Runs until the coordinator's
// segment-update channel is closed. Only fires anything when the
// session was started with two-pass transcription enabled — see
// live.Config.StreamingEngine.
func (a *App) emitSegmentUpdates() {
	if a.coordinator == nil {
		return
	}

	for seg := range a.coordinator.SegmentUpdates() {
		a.mu.Lock()
		if a.currentSess != nil {
			for i := range a.currentSess.Segments {
				if a.currentSess.Segments[i].ID == seg.ID {
					a.currentSess.Segments[i] = seg
					break
				}
			}
		}
		a.mu.Unlock()

		wailsRuntime.EventsEmit(a.ctx, "transcript:segment:update", seg)
	}
}
