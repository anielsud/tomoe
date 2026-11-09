package session

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTeams(t *testing.T, base, id, body string) {
	t.Helper()
	dir := filepath.Join(base, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, teamsFileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTeamsTranscript(t *testing.T) {
	const good = `{"writer":"musubi","source":"teams","subject":"Planning sync","start":"2026-11-06T10:00:00Z","end":"2026-11-06T10:30:00Z","turns":[{"speaker":"Robin Vale","offset":31.5,"text":"hello"},{"speaker":"Kai Moreno","offset":12,"text":"hi"}]}`
	tests := []struct {
		name  string
		file  string // "" means no teams.json
		turns int    // 0 means none expected
	}{
		{"missing file", "", 0},
		{"good file", good, 2},
		{"wrong writer", strings.Replace(good, `"musubi"`, `"someone-else"`, 1), 0},
		{"wrong source", strings.Replace(good, `"teams"`, `"zoom"`, 1), 0},
		{"no start", `{"writer":"musubi","source":"teams","turns":[{"speaker":"A B","offset":1}]}`, 0},
		{"no turns", `{"writer":"musubi","source":"teams","start":"2026-11-06T10:00:00Z","turns":[]}`, 0},
		{"bad turns dropped", `{"writer":"musubi","source":"teams","start":"2026-11-06T10:00:00Z","turns":[{"speaker":"","offset":1},{"speaker":"A B","offset":-3},{"speaker":"A B","offset":999999},{"speaker":"C D","offset":5}]}`, 1},
		{"not JSON", `writer: musubi`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			writeTeams(t, base, "s1", tt.file)
			got := NewStore(base).TeamsTranscript("s1")
			if tt.turns == 0 {
				if got != nil {
					t.Fatalf("got %d turns, want none", len(got.Turns))
				}
				return
			}
			if got == nil || len(got.Turns) != tt.turns {
				t.Fatalf("got %v, want %d turns", got, tt.turns)
			}
		})
	}
}

func TestTeamsTranscriptOrderAndCleaning(t *testing.T) {
	base := t.TempDir()
	writeTeams(t, base, "s1", `{"writer":"musubi","source":"teams","start":"2026-11-06T10:00:00Z","turns":[{"speaker":"Robin\u001b[31m  Vale\n","offset":31.5},{"speaker":"Kai Moreno","offset":12}]}`)
	store := NewStore(base)
	got := store.TeamsTranscript("s1")
	if got == nil {
		t.Fatal("no transcript")
	}
	if got.Turns[0].Speaker != "Kai Moreno" || got.Turns[1].Speaker != "Robin[31m Vale" {
		t.Errorf("turns = %q, %q", got.Turns[0].Speaker, got.Turns[1].Speaker)
	}
	for _, id := range []string{"", "..", "../s1", "s1/.."} {
		if store.TeamsTranscript(id) != nil {
			t.Errorf("TeamsTranscript(%q) read a file", id)
		}
	}
}

func TestTeamsSpans(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	tr := &TeamsTranscript{
		Start: start,
		End:   start.Add(200 * time.Second),
		Turns: []TeamsTurn{
			{Speaker: "A", Offset: 10},
			{Speaker: "B", Offset: 20},  // overlaps C: same start
			{Speaker: "C", Offset: 20},  // lasts to the next later turn
			{Speaker: "A", Offset: 30},  // 90 s gap: capped at 60 s
			{Speaker: "B", Offset: 180}, // capped at the transcript's end
		},
	}
	created := start.Add(-5 * time.Second) // recording began 5 s before Teams
	got := tr.Spans(created)
	want := []TeamsSpan{
		{"A", 15, 25, ""}, {"B", 25, 35, ""}, {"C", 25, 35, ""}, {"A", 35, 95, ""}, {"B", 185, 205, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Speaker != want[i].Speaker || math.Abs(got[i].Start-want[i].Start) > 1e-9 || math.Abs(got[i].End-want[i].End) > 1e-9 {
			t.Errorf("span %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestReadCalendarInvitees(t *testing.T) {
	dir := t.TempDir()
	if got := ReadCalendarInvitees(dir); got != nil {
		t.Errorf("no file: got %v", got)
	}
	body := `{"writer":"musubi","subject":"x","organizer":{"name":"Robin Vale","email":"r@example.com"},"invitees":[{"name":"Kai  Moreno","email":"k@example.com"},{"name":"robin vale"},{"name":""}]}`
	if err := os.WriteFile(filepath.Join(dir, calendarFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadCalendarInvitees(dir)
	if strings.Join(got, "|") != "Robin Vale|Kai Moreno" {
		t.Errorf("invitees = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, calendarFileName), []byte(strings.Replace(body, "musubi", "other", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadCalendarInvitees(dir); got != nil {
		t.Errorf("wrong writer: got %v", got)
	}
}
