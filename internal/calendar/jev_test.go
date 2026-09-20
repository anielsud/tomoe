package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imyousuf/CodeEagle/pkg/jev"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

func timeAt(year, month, day, hour, min int) time.Time {
	return time.Date(year, time.Month(month), day, hour, min, 0, 0, time.UTC)
}

const timeMinute = time.Minute

// fakeAsker is a minimal jev.Asker for unit tests. It records the last
// request and returns a canned Response or error.
type fakeAsker struct {
	lastState     any
	lastQuestions jev.Questions
	resp          *jev.Response
	err           error
	calls         int
}

func (f *fakeAsker) Ask(_ context.Context, state any, qs jev.Questions) (*jev.Response, error) {
	f.calls++
	f.lastState = state
	f.lastQuestions = qs
	return f.resp, f.err
}

// choiceResponse builds a jev.Response that returns the given probability
// distribution for the "match" Choice question. The highest-probability
// option is the "top" choice.
func choiceResponse(probs map[string]float64) *jev.Response {
	// Pick the winning option (highest probability).
	winner, best := "", -1.0
	for opt, p := range probs {
		if p > best {
			best = p
			winner = opt
		}
	}
	return &jev.Response{
		Model: "test",
		Answers: jev.Answers{
			"match": {
				Type:          "choice",
				Choice:        winner,
				Confidence:    best,
				Probabilities: probs,
			},
		},
	}
}

func defaultJevCfg() config.JevConfig {
	return config.JevConfig{
		Enabled:                  true,
		APIKey:                   "test-key",
		TopicWeight:              40,
		TranscriptContextSeconds: 120,
	}
}

func TestNewJevAdjudicatorDisabled(t *testing.T) {
	adj, err := NewJevAdjudicator(config.JevConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adj != nil {
		t.Errorf("expected nil adjudicator when disabled, got %+v", adj)
	}
}

func TestNewJevAdjudicatorRequiresAPIKey(t *testing.T) {
	adj, err := NewJevAdjudicator(config.JevConfig{Enabled: true, APIKey: ""})
	if err == nil {
		t.Error("expected error for empty api_key")
	}
	if adj != nil {
		t.Errorf("expected nil on error, got %+v", adj)
	}
}

func TestJevScoreYesWithMargin(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.80, "ambiguous": 0.15, "no": 0.05,
	})}
	j := newJevAdjudicator(asker, defaultJevCfg())
	got, err := j.Score(context.Background(), "transcript body", calendar.Event{Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 40 {
		t.Errorf("expected 40 (yes with margin), got %d", got)
	}
}

func TestJevScoreAmbiguousWithMargin(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.20, "ambiguous": 0.65, "no": 0.15,
	})}
	j := newJevAdjudicator(asker, defaultJevCfg())
	got, err := j.Score(context.Background(), "transcript", calendar.Event{Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 { // weight / 2
		t.Errorf("expected 20 (ambiguous with margin), got %d", got)
	}
}

func TestJevScoreNo(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.10, "ambiguous": 0.15, "no": 0.75,
	})}
	j := newJevAdjudicator(asker, defaultJevCfg())
	got, _ := j.Score(context.Background(), "transcript", calendar.Event{Title: "T"})
	if got != 0 {
		t.Errorf("expected 0 for 'no', got %d", got)
	}
}

func TestJevScoreBelowMarginReturnsZero(t *testing.T) {
	// Winner is "yes" but only by 0.05 over ambiguous — below the 0.15
	// margin threshold, so Jev's answer is untrustworthy → 0.
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.45, "ambiguous": 0.40, "no": 0.15,
	})}
	j := newJevAdjudicator(asker, defaultJevCfg())
	got, _ := j.Score(context.Background(), "transcript", calendar.Event{Title: "T"})
	if got != 0 {
		t.Errorf("expected 0 for below-margin winner, got %d", got)
	}
}

func TestJevScoreErrorPropagates(t *testing.T) {
	asker := &fakeAsker{err: errors.New("rate limited")}
	j := newJevAdjudicator(asker, defaultJevCfg())
	got, err := j.Score(context.Background(), "transcript", calendar.Event{Title: "T"})
	if err == nil {
		t.Error("expected error to propagate")
	}
	if got != 0 {
		t.Errorf("expected 0 on error, got %d", got)
	}
}

func TestJevScoreNilAdjudicatorSafe(t *testing.T) {
	var j *JevAdjudicator
	got, err := j.Score(context.Background(), "t", calendar.Event{})
	if err != nil || got != 0 {
		t.Errorf("nil adjudicator should return (0, nil), got (%d, %v)", got, err)
	}
}

func TestJevScoreCustomWeight(t *testing.T) {
	cfg := defaultJevCfg()
	cfg.TopicWeight = 10
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.9, "ambiguous": 0.05, "no": 0.05,
	})}
	j := newJevAdjudicator(asker, cfg)
	got, _ := j.Score(context.Background(), "t", calendar.Event{Title: "T"})
	if got != 10 {
		t.Errorf("expected weight override to 10, got %d", got)
	}
}

func TestJevScoreSendsTranscriptAndEventDetails(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{
		"yes": 0.9, "ambiguous": 0.05, "no": 0.05,
	})}
	j := newJevAdjudicator(asker, defaultJevCfg())
	_, _ = j.Score(context.Background(), "hello world transcript",
		calendar.Event{Title: "Q3 review", ParticipantCount: 2, Organizer: &calendar.Participant{Name: "Aniel Sharma"}})

	m, ok := asker.lastState.(map[string]any)
	if !ok {
		t.Fatalf("state = %T, want map[string]any", asker.lastState)
	}
	if m["transcript"] != "hello world transcript" {
		t.Errorf("transcript = %v", m["transcript"])
	}
	if m["event_title"] != "Q3 review" {
		t.Errorf("event_title = %v", m["event_title"])
	}
	if _, has := asker.lastQuestions["match"]; !has {
		t.Error("expected a 'match' question in the request")
	}
}

func TestTrimTranscript(t *testing.T) {
	long := ""
	for i := 0; i < 3000; i++ {
		long += "word "
	}
	got := trimTranscript(long, 60) // 60 * 15 = 900 bytes
	if len(got) > 900 {
		t.Errorf("trim did not respect limit: %d bytes", len(got))
	}
	if trimTranscript("short", 60) != "short" {
		t.Error("short transcript should pass through unchanged")
	}
	if trimTranscript("x", 0) != "x" {
		t.Error("ctxSecs=0 should pass through unchanged")
	}
}

func TestSummarizeAttendees(t *testing.T) {
	tests := []struct {
		name      string
		event     calendar.Event
		wantEmpty bool
	}{
		{"empty", calendar.Event{}, true},
		{"organizer only", calendar.Event{Organizer: &calendar.Participant{Name: "A"}, ParticipantCount: 1}, false},
		{"with participants", calendar.Event{
			Organizer: &calendar.Participant{Name: "A"},
			Participants: []calendar.Participant{
				{Name: "A", IsOrganizer: true},
				{Name: "B"},
				{Name: "C"},
			},
			ParticipantCount: 3,
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeAttendees(tt.event)
			if tt.wantEmpty && got != "unknown" {
				t.Errorf("expected 'unknown' for empty, got %q", got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("expected non-empty summary, got empty")
			}
		})
	}
}

// TestEnrichWithJevBoostsAmbiguousMatch verifies Jev integration end-to-end
// through the enricher: two heuristic-tied candidates go through Jev, one
// gets a positive boost and wins.
func TestEnrichWithJevBoostsAmbiguousMatch(t *testing.T) {
	base := timeAt(2026, 9, 20, 10, 0)
	ambiguous := &fakeProvider{
		name: "p",
		events: []calendar.Event{
			{Title: "Q3 review", StartTime: base, EndTime: base.Add(30 * timeMinute), MeetingURL: "https://meet.google.com/abc"},
			{Title: "Q4 planning", StartTime: base, EndTime: base.Add(30 * timeMinute), MeetingURL: "https://meet.google.com/xyz"},
		},
	}

	// Jev picks "Q3 review" ("yes"), rejects "Q4 planning" ("no").
	askerByTitle := &titleRoutedAsker{
		responses: map[string]*jev.Response{
			"Q3 review":   choiceResponse(map[string]float64{"yes": 0.85, "ambiguous": 0.10, "no": 0.05}),
			"Q4 planning": choiceResponse(map[string]float64{"yes": 0.05, "ambiguous": 0.10, "no": 0.85}),
		},
	}
	adj := newJevAdjudicator(askerByTitle, defaultJevCfg())
	en, _ := NewDefaultEnricher(defaultCalendarCfg(), []Provider{ambiguous})
	en.SetJevAdjudicator(adj)

	got, err := en.Enrich(context.Background(), calendar.MatchInput{
		SessionID:  "s-1",
		StartTime:  base,
		Platform:   "Meet",
		MeetingURL: "", // no URL from Wayland — matcher will find both events at heuristic 30 (time alignment only)
		Transcript: "aniel discussed q3 targets and revenue",
	})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got == nil {
		t.Fatal("expected a match after Jev boost")
	}
	if got.Title != "Q3 review" {
		t.Errorf("winner = %q, want Q3 review", got.Title)
	}
}

// TestEnrichSkipsJevWhenNoAmbiguity — Jev should not be invoked when the
// heuristic already produced an obvious winner.
func TestEnrichSkipsJevWhenNoAmbiguity(t *testing.T) {
	base := timeAt(2026, 9, 20, 10, 0)
	fp := &fakeProvider{
		name: "p",
		events: []calendar.Event{{
			Title:      "Winner",
			StartTime:  base,
			EndTime:    base.Add(30 * timeMinute),
			MeetingURL: "https://meet.google.com/abc",
		}},
	}
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{"yes": 1, "ambiguous": 0, "no": 0})}
	adj := newJevAdjudicator(asker, defaultJevCfg())
	en, _ := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	en.SetJevAdjudicator(adj)

	_, _ = en.Enrich(context.Background(), calendar.MatchInput{
		StartTime:  base,
		Platform:   "Meet",
		MeetingURL: "https://meet.google.com/abc",
		Transcript: "some transcript",
	})
	if asker.calls != 0 {
		t.Errorf("expected 0 jev calls when heuristic is unambiguous, got %d", asker.calls)
	}
}

// TestEnrichSkipsJevWhenNoTranscript — Jev needs transcript context to
// decide; without it, skip.
func TestEnrichSkipsJevWhenNoTranscript(t *testing.T) {
	base := timeAt(2026, 9, 20, 10, 0)
	fp := &fakeProvider{
		name: "p",
		events: []calendar.Event{{Title: "T", StartTime: base, EndTime: base.Add(30 * timeMinute)}},
	}
	asker := &fakeAsker{resp: choiceResponse(map[string]float64{"yes": 1, "ambiguous": 0, "no": 0})}
	adj := newJevAdjudicator(asker, defaultJevCfg())
	en, _ := NewDefaultEnricher(defaultCalendarCfg(), []Provider{fp})
	en.SetJevAdjudicator(adj)

	_, _ = en.Enrich(context.Background(), calendar.MatchInput{
		StartTime:  base,
		Platform:   "Meet",
		Transcript: "", // no transcript
	})
	if asker.calls != 0 {
		t.Errorf("expected 0 jev calls without transcript, got %d", asker.calls)
	}
}

// titleRoutedAsker returns a different Response per event title (the fake
// enricher happens to hand the title in via the state map). Lets one test
// exercise multi-candidate Jev calls.
type titleRoutedAsker struct {
	responses map[string]*jev.Response
	calls     int
}

func (r *titleRoutedAsker) Ask(_ context.Context, state any, _ jev.Questions) (*jev.Response, error) {
	r.calls++
	m, _ := state.(map[string]any)
	title, _ := m["event_title"].(string)
	if resp, ok := r.responses[title]; ok {
		return resp, nil
	}
	return choiceResponse(map[string]float64{"yes": 0, "ambiguous": 1, "no": 0}), nil
}
