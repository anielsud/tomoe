package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalendarSubject(t *testing.T) {
	const good = `{"writer":"musubi","subject":"Planning sync","start":"2026-11-06T10:00:00Z","end":"2026-11-06T10:30:00Z","joinUrl":"https://example.com/join","organizer":{"name":"Robin Vale","email":"robin@example.com"},"invitees":[{"name":"Kai Moreno","email":"kai@example.com"}]}`
	tests := []struct {
		name string
		file string // "" means no calendar.json
		want string
	}{
		{"missing file", "", ""},
		{"good file", good, "Planning sync"},
		{"wrong writer", `{"writer":"someone-else","subject":"Planning sync"}`, ""},
		{"no writer", `{"subject":"Planning sync"}`, ""},
		{"empty subject", `{"writer":"musubi","subject":""}`, ""},
		{"blank subject", `{"writer":"musubi","subject":"  \n "}`, ""},
		{"subject made one line", `{"writer":"musubi","subject":"  Planning\n  sync "}`, "Planning sync"},
		{"not JSON", `writer: musubi`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			store := NewStore(base)
			const id = "a1b2c3"
			dir := filepath.Join(base, id)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(dir, calendarFileName), []byte(tt.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := store.CalendarSubject(id); got != tt.want {
				t.Errorf("CalendarSubject = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCalendarSubjectLimits(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)
	dir := filepath.Join(base, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", maxCalendarSubject+50)
	if err := os.WriteFile(filepath.Join(dir, calendarFileName), []byte(`{"writer":"musubi","subject":"`+long+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := store.CalendarSubject("s1"); len([]rune(got)) != maxCalendarSubject {
		t.Errorf("long subject: got %d runes, want %d", len([]rune(got)), maxCalendarSubject)
	}
	for _, id := range []string{"", "..", "../s1", "s1/.."} {
		if got := store.CalendarSubject(id); got != "" {
			t.Errorf("CalendarSubject(%q) = %q, want none", id, got)
		}
	}
}
