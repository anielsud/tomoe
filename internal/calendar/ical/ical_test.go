package ical

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func openFixture(t *testing.T, name string) io.ReadCloser {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestParseICSExtractsSummaryAndTimes(t *testing.T) {
	f := openFixture(t, "personal.ics")

	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Hour)

	events, err := parseICS("Personal", f, from, to)
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events in window, want 2", len(events))
	}
	if events[0].Title != "Q3 review with Aniel" {
		t.Errorf("events[0].Title = %q", events[0].Title)
	}
	if events[0].MeetingURL != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("events[0].MeetingURL = %q", events[0].MeetingURL)
	}
	if events[1].Title != "Design review" {
		t.Errorf("events[1].Title = %q", events[1].Title)
	}
	if events[1].MeetingURL == "" {
		t.Errorf("events[1] should have a Zoom URL, got empty")
	}
}

func TestParseICSExtractsAttendees(t *testing.T) {
	f := openFixture(t, "personal.ics")
	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Hour)

	events, err := parseICS("Personal", f, from, to)
	if err != nil {
		t.Fatal(err)
	}
	first := events[0]
	if first.Organizer == nil || first.Organizer.Email != "aniel@example.com" || first.Organizer.Name != "Aniel Sharma" {
		t.Errorf("organizer: %+v", first.Organizer)
	}
	if !first.Organizer.IsOrganizer {
		t.Error("organizer.IsOrganizer should be true")
	}
	if len(first.Participants) != 2 {
		t.Errorf("participants count = %d, want 2", len(first.Participants))
	}
	if first.ParticipantCount != 2 {
		t.Errorf("ParticipantCount = %d, want 2", first.ParticipantCount)
	}
	// Chair role should also flag IsOrganizer on the matching attendee.
	var seenChair bool
	for _, p := range first.Participants {
		if p.Email == "aniel@example.com" {
			if !p.IsOrganizer {
				t.Errorf("attendee with ROLE=CHAIR should be IsOrganizer; got %+v", p)
			}
			seenChair = true
		}
	}
	if !seenChair {
		t.Error("did not find the CHAIR attendee")
	}
}

func TestParseICSNormalizesStatus(t *testing.T) {
	f := openFixture(t, "personal.ics")
	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Hour)

	events, _ := parseICS("Personal", f, from, to)
	second := events[1] // "Design review"
	statuses := map[string]string{}
	for _, p := range second.Participants {
		statuses[p.Email] = p.ResponseStatus
	}
	if statuses["priya@example.com"] != "accepted" {
		t.Errorf("priya = %q", statuses["priya@example.com"])
	}
	if statuses["imran@sosuke.ai"] != "tentative" {
		t.Errorf("imran = %q", statuses["imran@sosuke.ai"])
	}
	if statuses["aniel@example.com"] != "declined" {
		t.Errorf("aniel = %q", statuses["aniel@example.com"])
	}
}

func TestParseICSPrivacyStripped(t *testing.T) {
	f := openFixture(t, "privacy-stripped.ics")
	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Hour)

	events, err := parseICS("Personal", f, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Title != "Confidential 1:1" {
		t.Errorf("title = %q", e.Title)
	}
	if len(e.Participants) != 0 {
		t.Errorf("participants should be empty for a privacy-stripped feed; got %+v", e.Participants)
	}
	if e.ParticipantCount != 0 {
		t.Errorf("ParticipantCount = %d, want 0", e.ParticipantCount)
	}
}

func TestParseICSFiltersByWindow(t *testing.T) {
	f := openFixture(t, "personal.ics")
	// Window only covers the morning event.
	from := time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC)
	to := time.Date(2026, 9, 20, 11, 0, 0, 0, time.UTC)

	events, err := parseICS("Personal", f, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Title != "Q3 review with Aniel" {
		t.Errorf("got %d events; first title = %q; want just the Q3 review", len(events), func() string {
			if len(events) == 0 {
				return ""
			}
			return events[0].Title
		}())
	}
}

func TestParseICSMalformedReturnsError(t *testing.T) {
	_, err := parseICS("bad", strings.NewReader("not an ical"), time.Time{}, time.Time{}.Add(time.Hour))
	if err == nil {
		t.Error("expected error on malformed input")
	}
}

func TestNewRejectsEmptyArgs(t *testing.T) {
	if _, err := New("", "https://a", nil); err == nil {
		t.Error("empty name should be rejected")
	}
	if _, err := New("N", "", nil); err == nil {
		t.Error("empty URL should be rejected")
	}
	if _, err := New("N", "https://a", nil); err != nil {
		t.Errorf("valid args should not error: %v", err)
	}
}

func TestProviderListEventsOverHTTP(t *testing.T) {
	data, err := os.ReadFile("testdata/personal.ics")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, err := New("Personal", srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	events, err := p.ListEvents(t.Context(), from, from.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2", len(events))
	}
}

func TestProviderListEventsNon200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	p, err := New("Personal", srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ListEvents(t.Context(), time.Time{}, time.Now())
	if err == nil {
		t.Fatal("expected error for 401")
	}
}
