package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// fakeProvider returns a canned event list; optionally errors.
type fakeProvider struct {
	name   string
	events []calendar.Event
	err    error
	calls  int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) ListEvents(_ context.Context, _, _ time.Time) ([]calendar.Event, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func defaultCalendarCfg() config.CalendarConfig {
	return config.CalendarConfig{
		Enabled:                 true,
		MatchStartWindowMinutes: 15,
		MatchScoreThreshold:     40,
		CacheTTLSeconds:         300,
	}
}

func TestNewDefaultEnricherRequiresProviders(t *testing.T) {
	_, err := NewDefaultEnricher(defaultCalendarCfg(), nil)
	if err == nil {
		t.Fatal("expected error for zero providers")
	}
}

func TestEnrichReturnsBestMatch(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		name: "personal",
		events: []calendar.Event{{
			Title:      "Q3 review with Aniel",
			StartTime:  base,
			EndTime:    base.Add(30 * time.Minute),
			MeetingURL: "https://meet.google.com/abc-defg-hij",
		}},
	}
	en, err := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	if err != nil {
		t.Fatal(err)
	}
	got, err := en.Enrich(context.Background(), calendar.MatchInput{
		SessionID:  "s-1",
		StartTime:  base.Add(2 * time.Minute),
		Platform:   "Meet",
		MeetingURL: "https://meet.google.com/abc-defg-hij",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a match")
	}
	if got.Title != "Q3 review with Aniel" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Provider != "personal" {
		t.Errorf("Provider = %q, want %q", got.Provider, "personal")
	}
	if got.MatchScore < 40 {
		t.Errorf("MatchScore = %d, want ≥ 40", got.MatchScore)
	}
	if got.MatchedAt.IsZero() {
		t.Error("MatchedAt should be set")
	}
}

func TestEnrichBelowThresholdReturnsNil(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		name: "personal",
		events: []calendar.Event{{
			Title:     "Some meeting",
			StartTime: base.Add(20 * time.Minute), // outside 15-min window
			EndTime:   base.Add(50 * time.Minute),
		}},
	}
	en, err := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	if err != nil {
		t.Fatal(err)
	}
	got, err := en.Enrich(context.Background(), calendar.MatchInput{StartTime: base})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected no match, got %+v", got)
	}
}

func TestEnrichIgnoresProviderErrors(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	good := &fakeProvider{
		name: "good",
		events: []calendar.Event{{
			Title:      "T",
			StartTime:  base,
			EndTime:    base.Add(30 * time.Minute),
			MeetingURL: "https://meet.google.com/xyz",
		}},
	}
	bad := &fakeProvider{name: "bad", err: errors.New("network gone")}
	en, err := NewDefaultEnricher(defaultCalendarCfg(), []Provider{bad, good})
	if err != nil {
		t.Fatal(err)
	}
	// URL match (20) + platform (10) + start-alignment (30) = 60 > threshold 40.
	got, err := en.Enrich(context.Background(), calendar.MatchInput{
		StartTime: base, Platform: "Meet", MeetingURL: "https://meet.google.com/xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "good" {
		t.Errorf("expected 'good' match despite bad provider error; got %+v", got)
	}
}

func TestEnrichCacheReusesResults(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		name:   "p",
		events: []calendar.Event{{Title: "T", StartTime: base, EndTime: base.Add(30 * time.Minute)}},
	}
	en, err := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	if err != nil {
		t.Fatal(err)
	}
	in := calendar.MatchInput{StartTime: base}

	_, _ = en.Enrich(context.Background(), in)
	_, _ = en.Enrich(context.Background(), in)
	_, _ = en.Enrich(context.Background(), in)

	if fp.calls != 1 {
		t.Errorf("expected 1 provider call across 3 Enrichs (cached), got %d", fp.calls)
	}
}

func TestEnrichNilInputSafe(t *testing.T) {
	fp := &fakeProvider{name: "p"}
	en, _ := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	got, err := en.Enrich(context.Background(), calendar.MatchInput{})
	if err != nil {
		t.Errorf("zero MatchInput should not error: %v", err)
	}
	if got != nil {
		t.Errorf("zero MatchInput should return nil, got %+v", got)
	}
}

func TestEnricherProviderList(t *testing.T) {
	en, _ := NewDefaultEnricher(defaultCalendarCfg(),
		[]Provider{&fakeProvider{name: "a"}, &fakeProvider{name: "b"}})
	got := en.ProviderList()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ProviderList = %v, want [a b]", got)
	}
}
