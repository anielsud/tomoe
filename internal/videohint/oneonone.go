package videohint

import (
	"fmt"
	"strings"
	"time"
)

// A 1:1 call (calling someone, e.g. from a chat, as opposed to a meeting)
// has its own layout: the other person as a round avatar or a full video,
// no active-speaker ring, their name at the bottom left, and a toolbar
// with calling controls (Hold, Transfer, Dial pad, Consult) that meetings
// don't have. There is only one other person, so every remote voice is
// theirs: the name doesn't depend on who's speaking.

// callCheckFor is how long a window's toolbar reading is reused: OCR of
// the toolbar costs more than a look, and a call doesn't change kind.
const callCheckFor = 10 * time.Second

// The toolbar strip, in points from the top, read for calling controls.
const callToolbarY0, callToolbarY1 = 40, 90

type callCheck struct {
	windowID, width int
	at              time.Time
	call            bool
}

// isCallToolbar reports whether OCR'd toolbar text has the calling
// controls only 1:1 calls show: Hold together with Transfer, Dial pad or
// Consult (a meeting's toolbar has none of them).
func isCallToolbar(text string) bool {
	t := strings.ToLower(text)
	if !strings.Contains(t, "hold") {
		return false
	}
	return strings.Contains(t, "transfer") || strings.Contains(t, "dial pad") || strings.Contains(t, "consult")
}

// nameFromCallTitle is the person a 1:1 call window is titled for
// ("Microsoft Teams — Alex Kim | Microsoft Teams" -> "Alex Kim"), or "".
func nameFromCallTitle(window string) string {
	t := window
	if i := strings.Index(t, " — "); i >= 0 {
		t = t[i+len(" — "):]
	}
	t = strings.TrimSpace(strings.TrimSuffix(t, "| Microsoft Teams"))
	if t == "" || strings.Contains(t, "|") || strings.EqualFold(t, "Microsoft Teams") {
		return ""
	}
	return t
}

// isOneOnOneCall reports whether fr is a 1:1 call window, reading its
// toolbar at most every callCheckFor per window.
func (w *Watcher) isOneOnOneCall(fr *frame, at time.Time) bool {
	c := &w.call
	if c.windowID == fr.windowID && c.width == fr.width && at.Sub(c.at) < callCheckFor {
		return c.call
	}
	scale := max(1, fr.scale)
	strip, sw, sh, err := cropRGB(fr.pix, fr.width, fr.height, 0, callToolbarY0*scale, fr.width, (callToolbarY1-callToolbarY0)*scale)
	call := false
	if err == nil {
		if text, err := RecognizeText(strip, sw, sh); err == nil {
			call = isCallToolbar(text)
		}
	}
	*c = callCheck{windowID: fr.windowID, width: fr.width, at: at, call: call}
	return call
}

// oneOnOne names the other person in a 1:1 call: the label at the bottom
// left of the stage (where speaker view's name sits), else the window's
// title. ok is false when fr isn't a 1:1 call or no name was found.
func (w *Watcher) oneOnOne(l *Look, fr *frame, rule Rule) (ok bool) {
	if !w.isOneOnOneCall(fr, l.Time) {
		return false
	}
	name := ""
	if rule.Label.configured() {
		main := RingMatch{X: stageInset, Y: stageTop, Width: fr.width - 2*stageInset, Height: fr.height - stageTop - stageInset}
		if n, err := RecognizeLabel(fr.pix, fr.width, fr.height, main, rule.Label); err == nil {
			name = n
		}
	}
	from := "the label under the stage"
	if name == "" {
		name, from = nameFromCallTitle(l.Window), "the window title"
	}
	if name == "" {
		return false
	}
	l.Stage, l.Name, l.Usable = StageOneOnOne, name, true
	l.Detail = fmt.Sprintf("1:1 call (calling controls in the toolbar): the other person is %q, from %s", name, from)
	return true
}
