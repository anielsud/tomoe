//go:build darwin

package videohint

import (
	"fmt"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

const captureSupported = true

// captureWindow captures the window source names (see WatchConfig.Source)
// and the platform whose rule applies to it. The frame is nil when there's
// nothing to look at, with the stage saying why.
func captureWindow(source string) (fr *frame, platform meeting.Platform, window string, stage EventStage, detail string) {
	var id teamsvideo.WindowID
	var pick string
	var pointsWidth, pid int
	switch source {
	case SourceNone:
		return nil, "", "", StageWindowNotFound, "video hints are off"
	case SourceAuto:
		// Zoom's call window exists only during a call, so it comes first;
		// any Teams window (a chat, the calendar) can pass for a candidate
		// until its frame is checked for call chrome.
		ws, _ := teamsvideo.ListWindowsWithFloating()
		if zw, ok := pickZoomCall(ws, false); ok {
			return captureZoom(zw)
		}
		rec, f, why, err := pickCallWindow()
		if err != nil {
			// No Teams window: a minimized Zoom call's thumbnail, if any.
			if zw, ok := pickZoomCall(ws, true); ok {
				return captureZoom(zw)
			}
			return nil, "", "", StageWindowNotFound, "no Teams or Zoom meeting window on screen: " + why
		}
		platform, window = meeting.PlatformTeams, "Microsoft Teams — "+rec.Title
		if f == nil {
			return nil, platform, window, StageCaptureFailed, why
		}
		return &frame{width: f.Width, height: f.Height, pix: f.Pix, windowID: rec.ID, pick: why, scale: captureScale(f.Width, rec.Width)}, platform, window, StageFrameCaptured, fmt.Sprintf("captured %dx%d", f.Width, f.Height)
	default:
		w, err := teamsvideo.FindWindowByOwner(source)
		if err != nil {
			return nil, "", "", StageWindowNotFound, fmt.Sprintf("no %s window on screen", source)
		}
		if strings.Contains(strings.ToLower(source), "zoom") {
			// Zoom's largest window may be its home screen, not the call.
			all, _ := teamsvideo.ListWindowsWithFloating()
			zw, ok := pickZoomWindow(all, w.Owner)
			if !ok {
				return nil, meeting.PlatformZoom, "", StageWindowNotFound, "Zoom's call window isn't on screen"
			}
			w = zw
		}
		id, window, pointsWidth = w.ID, w.Owner, w.Width
		if w.Title != "" {
			window += " — " + w.Title
		}
		switch owner := strings.ToLower(w.Owner); {
		case strings.Contains(owner, "teams"):
			platform = meeting.PlatformTeams
		case strings.Contains(owner, "zoom"):
			platform = meeting.PlatformZoom
		}
		pid = w.OwnerPID
	}
	f, err := teamsvideo.CaptureWindowRGB(id)
	if err != nil {
		return nil, platform, window, StageCaptureFailed, err.Error()
	}
	return &frame{width: f.Width, height: f.Height, pix: f.Pix, windowID: int(id), pick: pick, scale: captureScale(f.Width, pointsWidth), pid: pid}, platform, window, StageFrameCaptured, fmt.Sprintf("captured %dx%d", f.Width, f.Height)
}

// Windows lists the on-screen app windows video hints could watch.
func Windows() ([]WindowChoice, error) {
	ws, err := teamsvideo.ListWindows()
	if err != nil {
		return nil, err
	}
	var out []WindowChoice
	seen := map[string]bool{}
	for _, w := range ws {
		if w.Owner == "" || seen[w.Owner] {
			continue
		}
		seen[w.Owner] = true
		out = append(out, WindowChoice{App: w.Owner, Title: w.Title, Known: strings.Contains(strings.ToLower(w.Owner), "teams")})
	}
	return out, nil
}

// snapshotWindows lists every on-screen window, front to back.
func snapshotWindows() []teamsvideo.WindowRecord {
	recs, _ := teamsvideo.ListAllWindows()
	return recs
}

// captureWindowByID captures one window.
func captureWindowByID(id int) (*frame, error) {
	f, err := teamsvideo.CaptureWindowRGB(teamsvideo.WindowID(id))
	if err != nil {
		return nil, err
	}
	return &frame{width: f.Width, height: f.Height, pix: f.Pix, windowID: id}, nil
}

// pickCallWindow chooses the Teams window to read in automatic mode. The
// window rule (teamsvideo.MeetingCandidates) lists the acceptable windows
// frontmost first; the first that captures something (not black) and
// shows a live call's controls wins. Failing that, the first that
// captured anything, then the frontmost. A floating compact view, which
// captures black, or a Calendar window in front of the call, are skipped
// this way instead of being watched while the call sits behind them. The
// returned sentence says what was tried.
func pickCallWindow() (teamsvideo.WindowRecord, *teamsvideo.Frame, string, error) {
	recs, err := teamsvideo.ListAllWindows()
	if err != nil {
		return teamsvideo.WindowRecord{}, nil, "", err
	}
	cands := teamsvideo.MeetingCandidates(recs)
	if len(cands) == 0 {
		_, why := teamsvideo.PickMeetingWindow(recs)
		return teamsvideo.WindowRecord{}, nil, why, fmt.Errorf("no candidate")
	}
	rule, hasRule := ruleFor(meeting.PlatformTeams)
	var notes []string
	// Last resorts, in order: the first window that captured something
	// but showed no call controls, then the frontmost one that captured.
	var noControls, front *teamsvideo.WindowRecord
	var noControlsFrame, frontFrame *teamsvideo.Frame
	for n, i := range cands {
		rec := recs[i]
		f, err := teamsvideo.CaptureWindowRGB(teamsvideo.WindowID(rec.ID))
		if err != nil {
			notes = append(notes, fmt.Sprintf("%d %q: capture failed", rec.ID, rec.Title))
			continue
		}
		if front == nil {
			r := rec
			front, frontFrame = &r, f
		}
		switch {
		case isBlank(f.Pix, f.Width, f.Height):
			notes = append(notes, fmt.Sprintf("%d %q (%dx%d, layer %d, sharing %d): captured blank", rec.ID, rec.Title, rec.Width, rec.Height, rec.Layer, rec.Sharing))
		case hasRule && rule.Chrome.configured() && !DetectCallChrome(f.Pix, f.Width, f.Height, rule.Chrome):
			notes = append(notes, fmt.Sprintf("%d %q: no call controls", rec.ID, rec.Title))
			if noControls == nil {
				r := rec
				noControls, noControlsFrame = &r, f
			}
		default:
			why := fmt.Sprintf("picked window %d %q (%dx%d, layer %d, candidate %d of %d)", rec.ID, rec.Title, rec.Width, rec.Height, rec.Layer, n+1, len(cands))
			if len(notes) > 0 {
				why += "; passed over: " + strings.Join(notes, "; ")
			}
			return rec, f, why, nil
		}
	}
	switch {
	case noControls != nil:
		return *noControls, noControlsFrame, "no candidate is both readable and showing call controls; using the first readable one: " + strings.Join(notes, "; "), nil
	case front != nil:
		return *front, frontFrame, "no candidate captured anything but black; using the frontmost: " + strings.Join(notes, "; "), nil
	}
	return recs[cands[0]], nil, "every candidate failed to capture: " + strings.Join(notes, "; "), nil
}

// captureScale is a capture's pixels per point: the captured width over
// the window's width in points, rounded (1 when the latter is unknown).
func captureScale(pixels, points int) int {
	if points <= 0 || pixels <= 0 {
		return 1
	}
	return max(1, (pixels+points/2)/points)
}

// pickZoomWindow is the window of owner to watch: the call window, or,
// while it's minimized, the untitled floating thumbnail Zoom shows the
// shared screen in (small, but a record of what was shown). Never the home
// screen ("Zoom Workplace"). ws is largest first.
func pickZoomWindow(ws []teamsvideo.WindowInfo, owner string) (teamsvideo.WindowInfo, bool) {
	for _, w := range ws {
		if w.Owner == owner && w.Title == zoomMeetingWindow {
			return w, true
		}
	}
	for _, w := range ws {
		if w.Owner == owner && w.Title == "" {
			return w, true
		}
	}
	return teamsvideo.WindowInfo{}, false
}

// zoomThumbMaxWidth is the widest Zoom's floating thumbnail of a
// minimized call gets (it's 240 points).
const zoomThumbMaxWidth = 400

// pickZoomCall finds a Zoom call among all windows without being told
// which app to watch: the call window, or with thumb the floating
// thumbnail of a minimized one (untitled and small, unlike Zoom's other
// untitled windows).
func pickZoomCall(ws []teamsvideo.WindowInfo, thumb bool) (teamsvideo.WindowInfo, bool) {
	for _, w := range ws {
		if !strings.Contains(strings.ToLower(w.Owner), "zoom") {
			continue
		}
		if w.Title == zoomMeetingWindow || (thumb && w.Title == "" && w.Width <= zoomThumbMaxWidth) {
			return w, true
		}
	}
	return teamsvideo.WindowInfo{}, false
}

// captureZoom captures Zoom window zw.
func captureZoom(zw teamsvideo.WindowInfo) (*frame, meeting.Platform, string, EventStage, string) {
	window := zw.Owner + " — " + zw.Title
	f, err := teamsvideo.CaptureWindowRGB(zw.ID)
	if err != nil {
		return nil, meeting.PlatformZoom, window, StageCaptureFailed, err.Error()
	}
	return &frame{width: f.Width, height: f.Height, pix: f.Pix, windowID: int(zw.ID), scale: captureScale(f.Width, zw.Width), pid: zw.OwnerPID}, meeting.PlatformZoom, window, StageFrameCaptured, fmt.Sprintf("captured %dx%d", f.Width, f.Height)
}
