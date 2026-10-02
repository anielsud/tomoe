package teamsvideo

import (
	"fmt"
	"strings"
)

// WindowRecord is one on-screen window as the window server lists it:
// every window, of every app and layer, front to back (Order 0 is the
// frontmost). Recorded with Record for tuning to see why a window was or
// wasn't picked for video hints.
type WindowRecord struct {
	ID     int    `json:"id"`
	PID    int    `json:"pid"`
	Owner  string `json:"owner"`
	Title  string `json:"title"`
	Layer  int    `json:"layer"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Order  int    `json:"order"`
	// Sharing is the window server's sharing state: 0 means the app has
	// asked not to be captured (such a window comes back black), 1 read
	// only, 2 read and write.
	Sharing int `json:"sharing"`
}

// IsTeams reports whether the window belongs to Microsoft Teams.
func (w WindowRecord) IsTeams() bool {
	return strings.Contains(strings.ToLower(w.Owner), "teams")
}

// qualifies reports whether the window rule accepts w, or else why not
// ("untitled", "chat", "generic").
func qualifies(w WindowRecord) (bool, string) {
	switch {
	case !w.IsTeams():
		return false, ""
	case w.Title == "":
		return false, "untitled"
	case strings.HasPrefix(w.Title, "Chat |"):
		return false, "chat"
	case w.Title == "Window":
		return false, "generic"
	case w.ID < 0:
		return false, "invalid"
	}
	return true, ""
}

// MeetingCandidates is the indexes in recs of every window the rule
// accepts, frontmost first. The first is what PickMeetingWindow returns;
// video hints try the rest when it can't be read (a floating compact
// view captures black, a Calendar window shows no call).
func MeetingCandidates(recs []WindowRecord) []int {
	var out []int
	for i, w := range recs {
		if ok, _ := qualifies(w); ok {
			out = append(out, i)
		}
	}
	return out
}

// PickMeetingWindow is the rule video hints start from in automatic mode:
// the first (frontmost) Teams window whose title isn't empty, "Window",
// or a chat panel ("Chat |"). It returns that window's index in recs (-1
// if none) and a sentence saying what it picked and what it passed over,
// so a recording shows whether the rule is right.
func PickMeetingWindow(recs []WindowRecord) (int, string) {
	var untitled, chat, generic, teams int
	for i, w := range recs {
		ok, why := qualifies(w)
		if w.IsTeams() {
			teams++
		}
		switch why {
		case "untitled":
			untitled++
		case "chat":
			chat++
		case "generic":
			generic++
		}
		if ok {
			return i, fmt.Sprintf("picked window %d %q (%dx%d, layer %d, #%d front to back of %d windows); passed over before it: %s",
				w.ID, w.Title, w.Width, w.Height, w.Layer, w.Order, len(recs), skipped(untitled, chat, generic))
		}
	}
	return -1, fmt.Sprintf("no Teams window qualified (%d Teams windows of %d: %s)", teams, len(recs), skipped(untitled, chat, generic))
}

func skipped(untitled, chat, generic int) string {
	return fmt.Sprintf("%d untitled, %d \"Chat |\", %d titled \"Window\"", untitled, chat, generic)
}
