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
// either produces a naming hint or escalates it to the pending snapshot
// staging area (CaptureUnrecognizedUI / config.UnrecognizedUIPendingDir).
// Only PlatformTeams has a calibrated rule today (see rule.go); every
// other platform's rule table entry is still empty, so those always
// escalate — expected, not a bug.
//
// Every step is reported on events (one Event per stage reached this
// tick, in order — see EventStage) so a caller can show not just
// videohint's end result but how and when it got there: whether a
// window was found this tick, what size frame it captured, whether the
// ring matched and where, whether OCR read anything. Sends are
// non-blocking (see sendEvent) — a slow or absent consumer never stalls
// polling. events may be nil if the caller doesn't want the trace.
//
// A StageOCRHit Event's Name field is the only part of this a caller
// needs to act on (e.g. via speaker.Tracker.SetHintForRecent) — this
// package deliberately has no internal/speaker dependency itself; that
// wiring is the caller's job (internal/daemon, internal/backend), since
// only they hold both the Tracker and this goroutine's lifecycle.
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
func Poll(ctx context.Context, interval time.Duration, events chan<- Event) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastCapture time.Time
	captured := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			platform := meeting.PlatformTeams // only window-finder wired up so far

			windowID, err := teamsvideo.FindMeetingWindow()
			if err != nil {
				sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageWindowNotFound, Detail: "no meeting window found on screen"})
				continue // no meeting window on screen right now -- normal, not an error
			}

			frame, err := teamsvideo.CaptureWindowRGB(windowID)
			if err != nil {
				sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageCaptureFailed, Detail: err.Error()})
				continue
			}
			sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageFrameCaptured, Detail: fmt.Sprintf("captured %dx%d frame", frame.Width, frame.Height)})

			reason := "no rule configured for this platform"
			rule, ok := ruleFor(platform)
			if !ok {
				sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageNoRule, Detail: reason})
			} else if ring, found := DetectRing(frame.Pix, frame.Width, frame.Height, rule.Ring); found {
				sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageRingMatched, Detail: fmt.Sprintf("ring at (%d,%d) %dx%d, confidence %.2f", ring.X, ring.Y, ring.Width, ring.Height, ring.Confidence)})

				if !rule.Label.configured() {
					reason = "ring found but no label region configured"
					sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageNoLabelRegion, Detail: reason})
				} else {
					name, ocrErr := RecognizeLabel(frame.Pix, frame.Width, frame.Height, *ring, rule.Label)
					if ocrErr == nil && name != "" {
						sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageOCRHit, Detail: fmt.Sprintf("OCR read %q from the label region", name), Name: name})
						continue // got a usable hint -- nothing to escalate this tick
					}
					reason = "ring found but OCR produced no text"
					detail := reason
					if ocrErr != nil {
						detail = fmt.Sprintf("%s: %v", reason, ocrErr)
					}
					sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageOCRMiss, Detail: detail})
				}
			} else {
				reason = "no ring match found"
				sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageNoRingMatch, Detail: reason})
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
			sendEvent(events, Event{Time: time.Now(), Platform: platform, Stage: StageEscalated, Detail: fmt.Sprintf("captured snapshot for review (%s)", reason)})
			lastCapture = time.Now()
			captured++
		}
	}
}
