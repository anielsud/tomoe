package backend

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// defaultTitleTime is how a default title gives the session's start.
const defaultTitleTime = "2006-01-02 15:04"

// defaultTitle is a new session's title until a better name is known:
// "Teams Meeting 2026-11-06 10:02", or "Session 2026-11-06 10:02" when
// no meeting app was named.
func defaultTitle(platform string, at time.Time) string {
	if platform == "" {
		return "Session " + at.Format(defaultTitleTime)
	}
	return fmt.Sprintf("%s Meeting %s", platform, at.Format(defaultTitleTime))
}

// isDefaultTitle reports whether title is one defaultTitle makes.
func isDefaultTitle(title string) bool {
	n := len(defaultTitleTime)
	if len(title) < n+1 || title[len(title)-n-1] != ' ' {
		return false
	}
	if _, err := time.Parse(defaultTitleTime, title[len(title)-n:]); err != nil {
		return false
	}
	head := title[:len(title)-n-1]
	return head == "Session" || (strings.HasSuffix(head, " Meeting") && len(head) > len(" Meeting"))
}

// titleFromWindow names a session still carrying its default title for
// the meeting window the video-hint looks showed most (see
// videohint.MeetingTitle). A title the user set is kept, and so is the
// default when no look shows a meeting's name.
func (a *App) titleFromWindow(sess *session.Session) {
	if sess.TitleSet || !isDefaultTitle(sess.Title) {
		return
	}
	looks, err := videohint.ReadLooks(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil {
		fmt.Printf("session %s: reading looks for its title: %v\n", sess.ID, err)
	}
	if t := videohint.MeetingTitle(looks); t != "" {
		sess.Title = t
	}
}

// showCalendarTitle shows the subject of the calendar event the session
// was matched to (its calendar.json, written by the companion app) as
// its title, unless the user named the session. Only the copy handed to
// the app changes: session.json is left as it is.
func (a *App) showCalendarTitle(sess *session.Session) {
	if sess == nil || sess.TitleSet || a.store == nil {
		return
	}
	if subject := a.store.CalendarSubject(sess.ID); subject != "" {
		sess.Title = subject
	}
}
