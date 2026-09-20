package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	icalendar "github.com/sosuke-ai/tomoe-pc/internal/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// mockEnricher records the MatchInput it received and returns a canned
// event or error. Small, obvious, exactly the shape an external embedder
// would supply.
type mockEnricher struct {
	got   calendar.MatchInput
	event *calendar.Event
	err   error
	calls int
}

func (m *mockEnricher) Enrich(_ context.Context, in calendar.MatchInput) (*calendar.Event, error) {
	m.calls++
	m.got = in
	return m.event, m.err
}

func newTestApp(t *testing.T, en calendar.Enricher) *App {
	t.Helper()
	a := NewApp()
	a.SetCalendar(en)
	a.calendarStore = icalendar.NewStore(t.TempDir())
	return a
}

// TestRunCalendarEnrichmentAttachesMatchInputAndCachesEvent covers the
// happy path: the enricher receives the correct MatchInput, its returned
// event is cached under the session id, and session.json is NOT touched.
func TestRunCalendarEnrichmentAttachesMatchInputAndCachesEvent(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	sess := &session.Session{
		ID:        "sess-1",
		Platform:  "Meet",
		CreatedAt: start,
		EndedAt:   end,
	}
	returned := &calendar.Event{Title: "Q3 review", StartTime: start, EndTime: end}

	m := &mockEnricher{event: returned}
	a := newTestApp(t, m)

	a.runCalendarEnrichment(sess, "Q3 review — Chrome — https://meet.google.com/abc-defg-hij")

	if m.calls != 1 {
		t.Fatalf("expected 1 enrich call, got %d", m.calls)
	}
	if m.got.SessionID != "sess-1" {
		t.Errorf("MatchInput.SessionID = %q, want sess-1", m.got.SessionID)
	}
	if m.got.Platform != "Meet" {
		t.Errorf("MatchInput.Platform = %q, want Meet", m.got.Platform)
	}
	if !m.got.StartTime.Equal(start) {
		t.Errorf("MatchInput.StartTime = %v, want %v", m.got.StartTime, start)
	}
	if m.got.MeetingURL != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("MatchInput.MeetingURL = %q, want the Meet URL", m.got.MeetingURL)
	}
	if m.got.WindowTitle == "" {
		t.Error("MatchInput.WindowTitle should be populated")
	}

	// Cache should now hold the event.
	cached, err := a.calendarStore.Load("sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cached == nil || cached.CalendarEvent == nil {
		t.Fatalf("expected cached CalendarEvent, got %+v", cached)
	}
	if cached.CalendarEvent.Title != "Q3 review" {
		t.Errorf("cached title = %q, want Q3 review", cached.CalendarEvent.Title)
	}
	if cached.MeetingURL != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("cached MeetingURL = %q, want the Meet URL", cached.MeetingURL)
	}
}

// TestRunCalendarEnrichmentToleratesEnricherErrors covers the contract that
// enrichment failures are non-fatal — the cache still records the attempt
// so re-transcribe can retry.
func TestRunCalendarEnrichmentToleratesEnricherErrors(t *testing.T) {
	sess := &session.Session{
		ID:        "sess-2",
		CreatedAt: time.Now(),
		EndedAt:   time.Now(),
	}
	m := &mockEnricher{err: errors.New("network gone")}
	a := newTestApp(t, m)

	a.runCalendarEnrichment(sess, "any window title")

	cached, err := a.calendarStore.Load("sess-2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cached == nil {
		t.Fatal("expected an entry recording the attempt even on error")
	}
	if cached.CalendarEvent != nil {
		t.Errorf("no CalendarEvent should be cached on error; got %+v", cached.CalendarEvent)
	}
}

// TestRunCalendarEnrichmentSkipsWhenNoEnricher covers the calendar-disabled
// case: no cache entry is written, no calls are made, nothing changes.
func TestRunCalendarEnrichmentSkipsWhenNoEnricher(t *testing.T) {
	a := NewApp()
	a.calendarStore = icalendar.NewStore(t.TempDir())
	// a.calendar is nil.

	a.runCalendarEnrichment(&session.Session{ID: "sess-3", CreatedAt: time.Now()}, "title")

	cached, err := a.calendarStore.Load("sess-3")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cached != nil {
		t.Errorf("no cache entry should exist when enricher is nil; got %+v", cached)
	}
}

// TestWithCalendarAttachesCachedEvent covers the Wails-facing enrichment: a
// session with a cached match returns a sessionWithCalendar with the event
// populated. Session with no cache returns an unwrapped session equivalent.
func TestWithCalendarAttachesCachedEvent(t *testing.T) {
	a := NewApp()
	a.calendarStore = icalendar.NewStore(t.TempDir())

	// No cache entry → nil calendar_event.
	sess := &session.Session{ID: "s-none", Title: "T"}
	wrapped := a.withCalendar(sess)
	if wrapped.CalendarEvent != nil {
		t.Errorf("expected nil calendar_event when no cache, got %+v", wrapped.CalendarEvent)
	}

	// Add a cache entry, verify the field surfaces.
	ev := &calendar.Event{Title: "Cached title"}
	err := a.calendarStore.Save(&icalendar.CachedMatch{
		SessionID:     "s-cached",
		CalendarEvent: ev,
		UpdatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped = a.withCalendar(&session.Session{ID: "s-cached", Title: "T"})
	if wrapped.CalendarEvent == nil || wrapped.CalendarEvent.Title != "Cached title" {
		t.Errorf("expected cached event to surface; got %+v", wrapped.CalendarEvent)
	}
}

// TestSetCalendarSwaps covers the seam: an external embedder can install a
// different enricher, and subsequent enrichment calls hit it.
func TestSetCalendarSwaps(t *testing.T) {
	first := &mockEnricher{}
	second := &mockEnricher{}
	a := NewApp()
	a.calendarStore = icalendar.NewStore(t.TempDir())
	a.SetCalendar(first)
	a.runCalendarEnrichment(&session.Session{ID: "s-1"}, "t")
	if first.calls != 1 || second.calls != 0 {
		t.Errorf("first should have been called; first=%d second=%d", first.calls, second.calls)
	}
	a.SetCalendar(second)
	a.runCalendarEnrichment(&session.Session{ID: "s-2"}, "t")
	if second.calls != 1 {
		t.Errorf("second should have been called after swap; got %d", second.calls)
	}
}

// Compile-time assertion: mockEnricher satisfies the public interface.
var _ calendar.Enricher = (*mockEnricher)(nil)
