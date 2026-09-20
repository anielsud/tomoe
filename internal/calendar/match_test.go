package calendar

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

func TestScoreStartAlignment(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	window := 15 * time.Minute

	tests := []struct {
		name    string
		candidateStart time.Time
		sessionStart   time.Time
		wantAtLeast    int
		wantAtMost     int
	}{
		{"exact", base, base, 30, 30},
		{"1min lag", base, base.Add(time.Minute), 25, 30},
		{"7.5min lag = midpoint", base, base.Add(7*time.Minute + 30*time.Second), 14, 16},
		{"14min lag", base, base.Add(14 * time.Minute), 0, 5},
		{"16min lag (outside window)", base, base.Add(16 * time.Minute), 0, 0},
		{"session started earlier", base, base.Add(-5 * time.Minute), 15, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreStartAlignment(tt.candidateStart, tt.sessionStart, window)
			if got < tt.wantAtLeast || got > tt.wantAtMost {
				t.Errorf("score = %d, want in [%d, %d]", got, tt.wantAtLeast, tt.wantAtMost)
			}
		})
	}
}

func TestScoreURL(t *testing.T) {
	ev := calendar.Event{MeetingURL: "https://meet.google.com/abc-defg-hij"}

	if got := scoreURL(ev, ""); got != 0 {
		t.Errorf("empty session URL → %d, want 0", got)
	}
	if got := scoreURL(calendar.Event{}, "https://meet.google.com/abc-defg-hij"); got != 0 {
		t.Errorf("empty event URL → %d, want 0", got)
	}
	if got := scoreURL(ev, "https://meet.google.com/abc-defg-hij"); got != 20 {
		t.Errorf("exact match → %d, want 20", got)
	}
	if got := scoreURL(ev, "https://different.url/x"); got != 0 {
		t.Errorf("no match → %d, want 0", got)
	}
}

func TestScorePlatformHint(t *testing.T) {
	tests := []struct {
		platform  string
		eventURL  string
		wantScore int
	}{
		{"Meet", "https://meet.google.com/x", 10},
		{"Teams", "https://teams.microsoft.com/l/meetup-join/y", 10},
		{"Zoom", "https://us02web.zoom.us/j/123", 10},
		{"", "https://meet.google.com/x", 0},
		{"Meet", "", 0},
		{"Zoom", "https://meet.google.com/x", 0}, // mismatch
		{"Unknown", "https://x", 0},
	}
	for _, tt := range tests {
		got := scorePlatformHint(calendar.Event{MeetingURL: tt.eventURL}, tt.platform)
		if got != tt.wantScore {
			t.Errorf("platform=%q eventURL=%q → %d, want %d", tt.platform, tt.eventURL, got, tt.wantScore)
		}
	}
}

func TestScoreTotal(t *testing.T) {
	cfg := scoringConfig{startWindow: 15 * time.Minute}
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	ev := calendar.Event{
		StartTime:  base,
		EndTime:    base.Add(30 * time.Minute),
		MeetingURL: "https://meet.google.com/abc",
	}
	in := calendar.MatchInput{
		StartTime:  base.Add(2 * time.Minute),
		Platform:   "Meet",
		MeetingURL: "https://meet.google.com/abc",
	}
	got := score(ev, in, cfg)
	// Time alignment ≈ 26, URL 20, platform 10 → ~56
	if got < 50 || got > 60 {
		t.Errorf("total = %d, want ~56 (50-60)", got)
	}
}

func TestPickBestAboveThreshold(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	long := calendar.Event{StartTime: base, EndTime: base.Add(8 * time.Hour)}
	short := calendar.Event{StartTime: base, EndTime: base.Add(30 * time.Minute)}

	candidates := []scoredEvent{
		{event: long, total: 50, provider: "long"},
		{event: short, total: 50, provider: "short"},
	}
	best := pickBest(candidates, 40)
	if best == nil {
		t.Fatal("expected a match")
	}
	if best.provider != "short" {
		t.Errorf("tie-break should prefer shorter event; got %q", best.provider)
	}
}

func TestPickBestBelowThresholdReturnsNil(t *testing.T) {
	if best := pickBest([]scoredEvent{{total: 39}}, 40); best != nil {
		t.Errorf("expected nil at score < threshold, got %+v", best)
	}
	if best := pickBest([]scoredEvent{{total: 40}}, 40); best == nil {
		t.Error("expected match at score == threshold, got nil")
	}
}

func TestExtractMeetingURL(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Q3 review — Chrome — https://meet.google.com/abc-defg-hij", "https://meet.google.com/abc-defg-hij"},
		{"Zoom Meeting https://us02web.zoom.us/j/1234567890", "https://us02web.zoom.us/j/1234567890"},
		{"https://teams.microsoft.com/l/meetup-join/19%3ameeting_XYZ", "https://teams.microsoft.com/l/meetup-join/19%3ameeting_XYZ"},
		{"", ""},
		{"plain title with no URL", ""},
	}
	for _, tt := range tests {
		got := ExtractMeetingURL(tt.title)
		if got != tt.want {
			t.Errorf("ExtractMeetingURL(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestCandidateWindowUnbounded(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	from, to := candidateWindow(start, 15*time.Minute, false)
	if from != start.Add(-15*time.Minute) {
		t.Errorf("from = %v, want %v", from, start.Add(-15*time.Minute))
	}
	if !to.After(start.Add(2 * time.Hour)) {
		t.Errorf("to should be well past session start when unbounded; got %v", to)
	}
}

func TestCandidateWindowBounded(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	_, to := candidateWindow(start, 15*time.Minute, true)
	if to.Sub(start) > 3*time.Hour {
		t.Errorf("bounded window should be tighter; to = %v", to)
	}
}
