package calendar

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

func TestStoreSaveThenLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	m := &CachedMatch{
		SessionID:  "sess-1",
		WindowTitle: "Q3 review — Chrome",
		MeetingURL:  "https://meet.google.com/abc-defg-hij",
		CalendarEvent: &calendar.Event{
			Source:    "ical",
			Provider:  "Personal",
			Title:     "Q3 review with Aniel",
			StartTime: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 9, 20, 10, 30, 0, 0, time.UTC),
			MatchedAt: time.Date(2026, 9, 20, 10, 35, 0, 0, time.UTC),
		},
		UpdatedAt: time.Date(2026, 9, 20, 10, 35, 0, 0, time.UTC),
	}
	if err := s.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nil")
	}
	if got.SessionID != m.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, m.SessionID)
	}
	if got.MeetingURL != m.MeetingURL {
		t.Errorf("MeetingURL = %q, want %q", got.MeetingURL, m.MeetingURL)
	}
	if got.CalendarEvent == nil || got.CalendarEvent.Title != "Q3 review with Aniel" {
		t.Errorf("event mismatch: %+v", got.CalendarEvent)
	}
}

func TestStoreLoadMissingIsNilNil(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.Load("nonexistent")
	if err != nil {
		t.Errorf("expected nil error for missing entry, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil CachedMatch, got %+v", got)
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	m := &CachedMatch{SessionID: "sess-2", UpdatedAt: time.Now()}
	if err := s.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete("sess-2"); err != nil {
		t.Errorf("first delete: %v", err)
	}
	if err := s.Delete("sess-2"); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
}

func TestStoreSaveRejectsEmptyID(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Save(&CachedMatch{}); err == nil {
		t.Error("expected error for empty session id")
	}
	if err := s.Save(nil); err == nil {
		t.Error("expected error for nil match")
	}
}

func TestStoreLoadHandlesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	// Directly write garbage as the cache file.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{not: valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("corrupt")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestStoreSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	// Pre-populate a "good" file.
	m1 := &CachedMatch{SessionID: "sess-3", MeetingURL: "https://a.example", UpdatedAt: time.Now()}
	if err := s.Save(m1); err != nil {
		t.Fatal(err)
	}
	// Overwrite with a second Save.
	m2 := &CachedMatch{SessionID: "sess-3", MeetingURL: "https://b.example", UpdatedAt: time.Now()}
	if err := s.Save(m2); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("sess-3")
	if err != nil {
		t.Fatal(err)
	}
	if got.MeetingURL != "https://b.example" {
		t.Errorf("MeetingURL = %q, want %q", got.MeetingURL, "https://b.example")
	}
	// After both saves, there should be exactly one file (the atomic rename
	// left no temp behind, no orphaned .tmp).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		if e.Type().IsRegular() {
			files++
		}
	}
	if files != 1 {
		t.Errorf("expected 1 file, got %d (entries: %v)", files, entries)
	}
}

func TestDefaultStoreDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	got := DefaultStoreDir()
	want := "/custom/state/tomoe/calendar"
	if got != want {
		t.Errorf("DefaultStoreDir() = %q, want %q", got, want)
	}
}
