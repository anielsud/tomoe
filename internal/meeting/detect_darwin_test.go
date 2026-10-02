//go:build darwin

package meeting

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
)

func TestDiffStreams(t *testing.T) {
	prev := map[int]bool{10: true, 20: true}
	cur := map[int]bool{20: true, 30: true}
	got := diffStreams(prev, cur, paFacilitySourceOutput)
	var added, removed int
	for _, c := range got {
		if c.facility != paFacilitySourceOutput {
			t.Errorf("facility = %d, want source output", c.facility)
		}
		switch c.eventType {
		case paEventNew:
			added++
		case paEventRemove:
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("added=%d removed=%d, want 1 and 1 (30 appeared, 10 vanished)", added, removed)
	}
	if len(diffStreams(cur, cur, paFacilitySinkInput)) != 0 {
		t.Error("unchanged streams produced events")
	}
}

func TestStreamInfosNamesAndSelf(t *testing.T) {
	sources := []audiosources.Source{
		{PID: 1, Name: "Zoom", BundleID: "us.zoom.xos"},
		{PID: 2, Name: "Google Chrome", BundleID: "com.google.Chrome"},
		{PID: 3, Name: "Microsoft Teams", BundleID: "com.microsoft.teams2"},
		{PID: 4, Name: "Tomoe", BundleID: "ai.sosuke.tomoe"},
		{PID: 5, Name: "Some Game", BundleID: "com.example.game"},
	}
	got := streamInfos(sources, 4)
	if len(got) != 4 {
		t.Fatalf("got %d streams, want 4 (Tomoe excluded)", len(got))
	}
	want := map[int]Platform{1: PlatformZoom, 3: PlatformTeams, 5: PlatformUnknown}
	for _, s := range got {
		if p, ok := want[s.PID]; ok {
			if gotP := identifyPlatform(s.AppName, s.PID); gotP != p {
				t.Errorf("pid %d (%s): platform %s, want %s", s.PID, s.AppName, gotP, p)
			}
		}
		if s.PID == 4 {
			t.Error("self was not excluded")
		}
	}
	if !knownBrowserBinaries[got[1].AppName] {
		t.Errorf("Chrome's stream name %q is not a known browser", got[1].AppName)
	}
}

func TestMeetingTitle(t *testing.T) {
	titles := []string{"", "Inbox - Mail", "Meet - abc-defg-hij - Google Chrome", "Zoom"}
	if got := meetingTitle(titles); got != "Meet - abc-defg-hij - Google Chrome" {
		t.Errorf("meetingTitle = %q", got)
	}
	if got := meetingTitle([]string{"Inbox", ""}); got != "" {
		t.Errorf("meetingTitle with no meeting = %q, want empty", got)
	}
}
