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
	switch source {
	case SourceNone:
		return nil, "", "", StageWindowNotFound, "video hints are off"
	case SourceAuto:
		rec, why, err := teamsvideo.FindMeetingWindowInfo()
		if err != nil {
			return nil, "", "", StageWindowNotFound, "no Teams meeting window on screen: " + why
		}
		id, platform, window, pick = teamsvideo.WindowID(rec.ID), meeting.PlatformTeams, "Microsoft Teams — "+rec.Title, why
	default:
		w, err := teamsvideo.FindWindowByOwner(source)
		if err != nil {
			return nil, "", "", StageWindowNotFound, fmt.Sprintf("no %s window on screen", source)
		}
		id, window = w.ID, w.Owner
		if w.Title != "" {
			window += " — " + w.Title
		}
		if strings.Contains(strings.ToLower(w.Owner), "teams") {
			platform = meeting.PlatformTeams
		}
	}
	f, err := teamsvideo.CaptureWindowRGB(id)
	if err != nil {
		return nil, platform, window, StageCaptureFailed, err.Error()
	}
	return &frame{width: f.Width, height: f.Height, pix: f.Pix, windowID: int(id), pick: pick}, platform, window, StageFrameCaptured, fmt.Sprintf("captured %dx%d", f.Width, f.Height)
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
