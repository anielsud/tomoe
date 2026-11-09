package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestDefaultTitle(t *testing.T) {
	at := time.Date(2026, 11, 6, 10, 2, 0, 0, time.Local)
	if got := defaultTitle("Teams", at); got != "Teams Meeting 2026-11-06 10:02" {
		t.Errorf("defaultTitle(Teams) = %q", got)
	}
	if got := defaultTitle("", at); got != "Session 2026-11-06 10:02" {
		t.Errorf("defaultTitle() = %q", got)
	}
	for _, title := range []string{defaultTitle("Teams", at), defaultTitle("Zoom", at), defaultTitle("", at)} {
		if !isDefaultTitle(title) {
			t.Errorf("isDefaultTitle(%q) = false, want true", title)
		}
	}
	for _, title := range []string{
		"", "Planning sync", "Kai Moreno", " Meeting 2026-11-06 10:02", "Meeting 2026-11-06 10:02",
		"Teams Meeting 2026-11-06", "Teams Meeting 2026-13-06 10:02", "Planning sync 2026-11-06 10:02",
		"Teams Meeting 2026-11-06 10:02 notes",
	} {
		if isDefaultTitle(title) {
			t.Errorf("isDefaultTitle(%q) = true, want false", title)
		}
	}
}

// newTitleApp is an app whose store holds one session with stored title,
// and a calendar.json with subject when subject isn't "".
func newTitleApp(t *testing.T, title string, titleSet bool, subject string) *App {
	t.Helper()
	base := t.TempDir()
	app := NewApp()
	app.store = session.NewStore(base)
	if err := app.store.Save(&session.Session{ID: "s1", Title: title, TitleSet: titleSet, Platform: "Teams"}); err != nil {
		t.Fatal(err)
	}
	if subject != "" {
		cal := `{"writer":"musubi","subject":"` + subject + `"}`
		if err := os.WriteFile(filepath.Join(base, "s1", "calendar.json"), []byte(cal), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func storedTitle(t *testing.T, app *App) *session.Session {
	t.Helper()
	sess, err := app.store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestCalendarTitleShown(t *testing.T) {
	app := newTitleApp(t, "Teams Meeting 2026-11-06 10:02", false, "Planning sync")
	sess, err := app.LoadSession("s1")
	if err != nil || sess.Title != "Planning sync" {
		t.Fatalf("LoadSession title = %q (%v), want the calendar subject", sess.Title, err)
	}
	list, err := app.GetSessionList()
	if err != nil || len(list) != 1 || list[0].Title != "Planning sync" {
		t.Fatalf("GetSessionList = %+v (%v), want the calendar subject", list, err)
	}
	md, err := app.ExportSession("s1", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Planning sync"; !strings.Contains(md, want) {
		t.Errorf("export doesn't carry the calendar subject:\n%s", md)
	}
	if got := storedTitle(t, app).Title; got != "Teams Meeting 2026-11-06 10:02" {
		t.Errorf("session.json title = %q, want it unchanged", got)
	}
}

func TestCalendarTitleKeepsUserTitle(t *testing.T) {
	app := newTitleApp(t, "Robin's notes", true, "Planning sync")
	sess, err := app.LoadSession("s1")
	if err != nil || sess.Title != "Robin's notes" {
		t.Fatalf("LoadSession title = %q (%v), want the user's", sess.Title, err)
	}
}

func TestUpdateSessionWithCalendarTitle(t *testing.T) {
	// Editing only the platform sends the shown (calendar) title back:
	// not a rename.
	app := newTitleApp(t, "Teams Meeting 2026-11-06 10:02", false, "Planning sync")
	if err := app.UpdateSession("s1", "Planning sync", "Zoom"); err != nil {
		t.Fatal(err)
	}
	if s := storedTitle(t, app); s.TitleSet || s.Title != "Teams Meeting 2026-11-06 10:02" || s.Platform != "Zoom" {
		t.Errorf("after a platform edit: %+v", s)
	}
	// A new title is the user's, and wins over the calendar's.
	if err := app.UpdateSession("s1", "Design review", ""); err != nil {
		t.Fatal(err)
	}
	if s := storedTitle(t, app); !s.TitleSet || s.Title != "Design review" {
		t.Errorf("after a rename: %+v", s)
	}
	if sess, _ := app.LoadSession("s1"); sess.Title != "Design review" {
		t.Errorf("shown title after a rename = %q", sess.Title)
	}
}
