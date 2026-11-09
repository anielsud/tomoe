package videohint

import "strings"

// Window titles the meeting app adds around a meeting's name, removed
// before the name is used.
var (
	meetingTitlePrefixes = []string{"Meeting join |", "Meeting compact view |"}
	meetingTitleSuffixes = []string{"| Microsoft Teams", "| Pinned window"}
)

// Window titles that aren't a meeting: the app's other screens and
// helper windows. notMeetingTitles must match the whole (cleaned) title,
// notMeetingTitlePrefixes its start; both ignore case.
var (
	notMeetingTitles = []string{
		"Calendar", "Sharing Indicator", "Microsoft Teams", "teams.microsoft.com/v2/",
		"Zoom Meeting", "Zoom Workplace", "Zoom",
	}
	notMeetingTitlePrefixes = []string{"Calendar |", "Chat |", "Search |"}
)

// meetingNameFromWindow is the meeting a look's window is titled for,
// or "" when the window isn't a meeting app's meeting window. window is
// "<app> — <title>" (see Look.Window), e.g. "Microsoft Teams — Planning
// sync | Microsoft Teams" -> "Planning sync". A 1:1 call's window is
// titled for the other person ("Microsoft Teams — Kai Moreno"), which is
// kept: it names the call as well as a meeting title would.
func meetingNameFromWindow(window string) string {
	app, title, ok := strings.Cut(window, " — ")
	if !ok {
		return "" // the app alone, no window title
	}
	// Only a meeting app's own window: a browser or other app the
	// watcher was pointed at is titled for whatever page it shows.
	app = strings.ToLower(app)
	if !strings.Contains(app, "teams") && !strings.Contains(app, "zoom") {
		return ""
	}
	t := strings.TrimSpace(title)
	for changed := true; changed; {
		changed = false
		for _, p := range meetingTitlePrefixes {
			if len(t) >= len(p) && strings.EqualFold(t[:len(p)], p) {
				t, changed = strings.TrimSpace(t[len(p):]), true
			}
		}
		for _, s := range meetingTitleSuffixes {
			if len(t) >= len(s) && strings.EqualFold(t[len(t)-len(s):], s) {
				t, changed = strings.TrimSpace(t[:len(t)-len(s)]), true
			}
		}
	}
	if t == "" {
		return ""
	}
	for _, n := range notMeetingTitles {
		if strings.EqualFold(t, n) {
			return ""
		}
	}
	for _, p := range notMeetingTitlePrefixes {
		if len(t) >= len(p) && strings.EqualFold(t[:len(p)], p) {
			return ""
		}
	}
	return t
}

// MeetingTitle is the meeting's name as its window was titled in most of
// looks (cleaned up by meetingNameFromWindow; windows that aren't a
// meeting are passed over), or "" when no look shows one. A tie goes to
// the name seen first.
func MeetingTitle(looks []Look) string {
	counts := map[string]int{}
	var order []string
	for _, l := range looks {
		name := meetingNameFromWindow(l.Window)
		if name == "" {
			continue
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	best := ""
	for _, name := range order {
		if counts[name] > counts[best] {
			best = name
		}
	}
	return best
}
