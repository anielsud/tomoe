package teamsvideo

import (
	"strings"
	"testing"
)

func TestPickMeetingWindow(t *testing.T) {
	recs := []WindowRecord{
		{ID: 1, Owner: "Finder", Title: "Downloads", Order: 0},
		{ID: 2, Owner: "Microsoft Teams", Title: "", Order: 1},
		{ID: 3, Owner: "Microsoft Teams", Title: "Chat | Alex | Microsoft Teams", Order: 2},
		{ID: 4, Owner: "Microsoft Teams", Title: "Window", Order: 3},
		{ID: 5, Owner: "Microsoft Teams", Title: "Calendar | Microsoft Teams", Order: 4},
		{ID: 6, Owner: "Microsoft Teams", Title: "Weekly sync | Microsoft Teams", Order: 5},
	}
	i, why := PickMeetingWindow(recs)
	// The rule takes the frontmost qualifying Teams window, which here is
	// the Calendar, not the call: that is exactly what a recording needs
	// to be able to show.
	if i != 4 {
		t.Fatalf("picked index %d, want 4 (Calendar, the frontmost titled non-chat window); %s", i, why)
	}
	for _, want := range []string{"window 5", "1 untitled", `1 "Chat |"`, `1 titled "Window"`} {
		if !strings.Contains(why, want) {
			t.Errorf("explanation %q lacks %q", why, want)
		}
	}
	if i, why := PickMeetingWindow(recs[:4]); i != -1 || !strings.Contains(why, "no Teams window qualified") {
		t.Errorf("no qualifying window: got %d, %q", i, why)
	}
	if i, _ := PickMeetingWindow(nil); i != -1 {
		t.Errorf("empty list picked %d", i)
	}
}
