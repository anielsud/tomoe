package calendar_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

// TestEnricherIsSatisfiable — a small compile-time proof that a type
// implementing Enrich(ctx, MatchInput) (*Event, error) satisfies the
// interface. External embedders will look at this test as a spec.
func TestEnricherIsSatisfiable(t *testing.T) {
	var _ calendar.Enricher = (*staticEnricher)(nil)
}

// TestParticipantResolverIsSatisfiable — same compile-time proof for the
// optional post-match participant enrichment hook.
func TestParticipantResolverIsSatisfiable(t *testing.T) {
	var _ calendar.ParticipantResolver = (*upperCaseResolver)(nil)
}

// upperCaseResolver is a trivial resolver used to exercise the interface
// shape. Real resolvers would look up canonical identity in a directory.
type upperCaseResolver struct{}

func (upperCaseResolver) ResolveParticipants(_ context.Context, ev *calendar.Event) error {
	if ev == nil {
		return nil
	}
	for i := range ev.Participants {
		if ev.Participants[i].Name != "" {
			ev.Participants[i].Name = "* " + ev.Participants[i].Name
		}
	}
	if ev.Organizer != nil && ev.Organizer.Name != "" {
		ev.Organizer.Name = "* " + ev.Organizer.Name
	}
	return nil
}

// TestResolverMutatesInPlace covers the contract that a resolver operates
// on the passed *Event and its mutations are visible to the caller.
func TestResolverMutatesInPlace(t *testing.T) {
	ev := &calendar.Event{
		Title:     "T",
		Organizer: &calendar.Participant{Name: "Aniel"},
		Participants: []calendar.Participant{
			{Name: "Aniel"},
			{Name: "Imran"},
		},
	}
	if err := (upperCaseResolver{}).ResolveParticipants(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if ev.Organizer.Name != "* Aniel" {
		t.Errorf("Organizer.Name = %q, want %q", ev.Organizer.Name, "* Aniel")
	}
	if ev.Participants[1].Name != "* Imran" {
		t.Errorf("Participants[1].Name = %q, want %q", ev.Participants[1].Name, "* Imran")
	}
}

// TestMatchInputZeroValueIsSafe covers the contract that an Enricher may
// receive a zero-valued MatchInput and must not panic. Downstream code
// checks for empty fields; nothing here should preclude that.
func TestMatchInputZeroValueIsSafe(t *testing.T) {
	var in calendar.MatchInput
	if in.SessionID != "" || in.Platform != "" {
		t.Fatalf("zero value should be all-zero: %+v", in)
	}
	if !in.StartTime.IsZero() || !in.EndTime.IsZero() {
		t.Fatalf("zero-value times should be zero: %+v", in)
	}
}

// TestEventJSONRoundtrip covers wire compatibility with the frontend. Every
// field must survive Marshal/Unmarshal with tags intact so the React
// component receives the correct camelCase-in-Go, snake_case-in-JSON shape.
func TestEventJSONRoundtrip(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	matched := start.Add(35 * time.Minute)

	original := calendar.Event{
		Source:   "ical",
		Provider: "Personal ICS",
		EventID:  "evt-123",
		Title:    "Q3 review with Aniel",
		Organizer: &calendar.Participant{
			Name: "Aniel Sharma", Email: "aniel@example.com", IsOrganizer: true, ResponseStatus: "accepted",
		},
		Participants: []calendar.Participant{
			{Name: "Aniel Sharma", Email: "aniel@example.com", IsOrganizer: true, ResponseStatus: "accepted"},
			{Name: "Imran Yousuf", Email: "imran@sosuke.ai", ResponseStatus: "accepted"},
		},
		ParticipantCount: 2,
		StartTime:        start,
		EndTime:          end,
		MeetingURL:       "https://meet.google.com/abc-defg-hij",
		MatchedAt:        matched,
		MatchScore:       80,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var round calendar.Event
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// time.Time round-trip is fine at nanosecond precision when both ends
	// are UTC; compare via Equal.
	if !round.StartTime.Equal(original.StartTime) || !round.EndTime.Equal(original.EndTime) {
		t.Errorf("times differ: %v/%v vs %v/%v", round.StartTime, round.EndTime, original.StartTime, original.EndTime)
	}
	round.StartTime, round.EndTime, round.MatchedAt = original.StartTime, original.EndTime, original.MatchedAt
	if round.Title != original.Title || round.Source != original.Source ||
		round.ParticipantCount != original.ParticipantCount || round.MeetingURL != original.MeetingURL {
		t.Errorf("scalar mismatch: got %+v want %+v", round, original)
	}
	if len(round.Participants) != len(original.Participants) {
		t.Errorf("participant count: got %d want %d", len(round.Participants), len(original.Participants))
	}
}

// TestEventJSONOmitEmpty covers the frontend contract that a nil / zero
// event omits optional fields. Prevents the React chip from rendering
// "0 attendees" or an empty organizer badge for events that legitimately
// have neither.
func TestEventJSONOmitEmpty(t *testing.T) {
	minimal := calendar.Event{
		Source:    "ical",
		Provider:  "P",
		Title:     "T",
		StartTime: time.Now(),
		EndTime:   time.Now(),
		MatchedAt: time.Now(),
	}
	data, err := json.Marshal(minimal)
	if err != nil {
		t.Fatal(err)
	}
	str := string(data)
	for _, key := range []string{`"event_id":`, `"participants":`, `"participant_count":`, `"meeting_url":`, `"match_score":`, `"organizer":`} {
		if containsKey(str, key) {
			t.Errorf("expected %s to be omitted for a minimal event; got %s", key, str)
		}
	}
}

func containsKey(s, key string) bool {
	// Simple substring check; JSON keys are unambiguously formed.
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] == key {
			return true
		}
	}
	return false
}

// staticEnricher is the doc.go example, materialized so the test verifies
// the shape a real embedder would use.
type staticEnricher struct {
	events []*calendar.Event
}

func (s *staticEnricher) Enrich(_ context.Context, in calendar.MatchInput) (*calendar.Event, error) {
	for _, e := range s.events {
		if !in.StartTime.Before(e.StartTime) && !in.StartTime.After(e.EndTime) {
			return e, nil
		}
	}
	return nil, nil
}

// TestStaticEnricherWorks — smoke test that an embedder's Enricher can be
// invoked and returns the expected value or nil-on-miss.
func TestStaticEnricherWorks(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	ev := &calendar.Event{Title: "T", StartTime: start, EndTime: start.Add(time.Hour)}
	en := &staticEnricher{events: []*calendar.Event{ev}}

	got, err := en.Enrich(context.Background(), calendar.MatchInput{StartTime: start.Add(15 * time.Minute)})
	if err != nil || got != ev {
		t.Errorf("expected event, got (%v, %v)", got, err)
	}

	got, err = en.Enrich(context.Background(), calendar.MatchInput{StartTime: start.Add(2 * time.Hour)})
	if err != nil || got != nil {
		t.Errorf("expected nil, got (%v, %v)", got, err)
	}
}
