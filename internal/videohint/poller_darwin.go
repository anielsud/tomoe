//go:build darwin

package videohint

import (
	"context"
	"fmt"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

const (
	// maxSnapshotsPerPoll caps how many escalation snapshots one Poll
	// call writes, so a long idle meeting doesn't fill the disk.
	maxSnapshotsPerPoll = 5
	// minSnapshotInterval rate-limits captures within that cap.
	minSnapshotInterval = 60 * time.Second
)

// Poll periodically looks for a window FindMeetingWindow's heuristic
// matches as Teams (the only window-finder wired up so far —
// Zoom/Meet/Webex/Slack each need their own before this can capture
// anything for them; see docs/macos-support.md), captures a frame, and
// either produces a naming hint or escalates it to the pending
// snapshot staging area (CaptureUnrecognizedUI /
// config.UnrecognizedUIPendingDir). Every platform's rule table entry
// is empty today (see rule.go), and there's no OCR until a later PR,
// so this always escalates in practice right now — that's expected,
// not a bug.
//
// Escalated snapshots are NOT the permanent library: FindMeetingWindow
// matches any non-trivial-titled Microsoft-Teams-owned window, which
// includes plain chat tabs, not just an actual call — confirmed live
// during this feature's own testing, capturing a real private chat
// conversation instead of a meeting. Nothing captured here is treated
// as safe to keep or use for calibration until a human explicitly
// reviews and approves it via `tomoe videohint approve` — see
// ApproveSnapshot.
//
// Blocks until ctx is cancelled; meant to be run in its own goroutine,
// one per live meeting session, cancelled when that session stops
// (see internal/daemon and internal/backend's meeting start/stop).
func Poll(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastCapture time.Time
	captured := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			windowID, err := teamsvideo.FindMeetingWindow()
			if err != nil {
				continue // no meeting window on screen right now -- normal, not an error
			}

			frame, err := teamsvideo.CaptureWindowRGB(windowID)
			if err != nil {
				continue
			}

			platform := meeting.PlatformTeams // only window-finder wired up so far
			reason := "no rule configured for this platform"
			if rule, ok := ruleFor(platform); ok {
				if _, found := DetectRing(frame.Pix, frame.Width, frame.Height, rule.Ring); found {
					// A ring alone has no name to attach yet -- OCR is
					// a later PR. Fall through to escalation for now.
					reason = "ring found but no OCR yet"
				} else {
					reason = "no ring match found"
				}
			}

			if captured >= maxSnapshotsPerPoll {
				continue
			}
			if !lastCapture.IsZero() && time.Since(lastCapture) < minSnapshotInterval {
				continue
			}

			meta := SnapshotMeta{
				Platform: platform,
				// WindowTitle/WindowOwner are left blank: FindMeetingWindow
				// doesn't expose what it matched internally, and adding
				// that lookup is out of scope for this change (teamsvideo
				// itself doesn't need modifying otherwise).
				Width:     frame.Width,
				Height:    frame.Height,
				Timestamp: time.Now(),
				Reason:    reason,
			}
			if err := CaptureUnrecognizedUI(meta, frame.Pix, frame.Width, frame.Height); err != nil {
				fmt.Printf("videohint: failed to capture snapshot: %v\n", err)
				continue
			}
			lastCapture = time.Now()
			captured++
		}
	}
}
