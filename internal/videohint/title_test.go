package videohint

import "testing"

func TestMeetingNameFromWindow(t *testing.T) {
	tests := []struct {
		window, want string
	}{
		{"Microsoft Teams — Planning sync", "Planning sync"},
		{"Microsoft Teams — Planning sync | Microsoft Teams", "Planning sync"},
		{"Microsoft Teams — Meeting join | Planning sync | Microsoft Teams", "Planning sync"},
		{"Microsoft Teams — Meeting compact view | Planning sync | Microsoft Teams", "Planning sync"},
		{"Microsoft Teams — Planning sync | Pinned window", "Planning sync"},
		{"Microsoft Teams — Meeting compact view | Planning sync | Microsoft Teams | Pinned window", "Planning sync"},
		{"Zoom — Design review", "Design review"},
		{"zoom.us — Design review", "Design review"},
		// A 1:1 call is titled for the other person.
		{"Microsoft Teams — Kai Moreno", "Kai Moreno"},
		{"Microsoft Teams — Kai Moreno | Microsoft Teams", "Kai Moreno"},
		// The app's other screens and helper windows.
		{"Microsoft Teams — Calendar", ""},
		{"Microsoft Teams — Calendar | Calendar | Microsoft Teams", ""},
		{"Microsoft Teams — Chat | Robin Vale | Microsoft Teams", ""},
		{"Microsoft Teams — Search | planning | Microsoft Teams", ""},
		{"Microsoft Teams — Sharing Indicator", ""},
		{"Microsoft Teams — Microsoft Teams", ""},
		{"Microsoft Teams — teams.microsoft.com/v2/", ""},
		{"Zoom — Zoom Meeting", ""},
		{"Zoom — Zoom Workplace", ""},
		{"Zoom — Zoom", ""},
		// No window title, nothing left after cleaning, or not a meeting app.
		{"Microsoft Teams", ""},
		{"Microsoft Teams — ", ""},
		{"Microsoft Teams —  | Microsoft Teams", ""},
		{"", ""},
		{"Google Chrome — Planning sync", ""},
	}
	for _, tt := range tests {
		if got := meetingNameFromWindow(tt.window); got != tt.want {
			t.Errorf("meetingNameFromWindow(%q) = %q, want %q", tt.window, got, tt.want)
		}
	}
}

func TestMeetingTitle(t *testing.T) {
	looks := func(windows ...string) []Look {
		out := make([]Look, len(windows))
		for i, w := range windows {
			out[i] = Look{ID: i + 1, Window: w}
		}
		return out
	}
	tests := []struct {
		name  string
		looks []Look
		want  string
	}{
		{"no looks", nil, ""},
		{"no window recorded", looks("", ""), ""},
		{"only non-meeting windows", looks(
			"Microsoft Teams — Calendar | Calendar | Microsoft Teams",
			"Microsoft Teams — Chat | Robin Vale | Microsoft Teams",
			"Microsoft Teams — Sharing Indicator",
		), ""},
		{"one meeting, title shapes merged", looks(
			"Microsoft Teams — Meeting join | Planning sync | Microsoft Teams",
			"Microsoft Teams — Calendar | Calendar | Microsoft Teams",
			"Microsoft Teams — Planning sync | Microsoft Teams",
			"Microsoft Teams — Meeting compact view | Planning sync | Microsoft Teams | Pinned window",
			"Microsoft Teams — Sharing Indicator",
		), "Planning sync"},
		{"the window shown most wins", looks(
			"Microsoft Teams — Chat | Kai Moreno | Microsoft Teams",
			"Microsoft Teams — Planning sync | Microsoft Teams",
			"Microsoft Teams — Design review | Microsoft Teams",
			"Microsoft Teams — Design review | Microsoft Teams",
			"Microsoft Teams — Calendar",
			"Microsoft Teams — Calendar",
			"Microsoft Teams — Calendar",
		), "Design review"},
		{"a tie goes to the first seen", looks(
			"Zoom — Design review",
			"Zoom — Planning sync",
			"Zoom — Zoom Meeting",
			"Zoom — Planning sync",
			"Zoom — Design review",
		), "Design review"},
		{"1:1 call", looks(
			"Microsoft Teams — Kai Moreno",
			"Microsoft Teams — Kai Moreno | Microsoft Teams",
			"Microsoft Teams — Microsoft Teams",
		), "Kai Moreno"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MeetingTitle(tt.looks); got != tt.want {
				t.Errorf("MeetingTitle = %q, want %q", got, tt.want)
			}
		})
	}
}
