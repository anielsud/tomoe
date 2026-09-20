package backend

import (
	"errors"
	"fmt"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	icalendar "github.com/sosuke-ai/tomoe-pc/internal/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/calendar/ical"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// buildCalendarEnricher constructs the default calendar.Enricher from a
// CalendarConfig. Called from Startup when calendar is enabled and no
// external embedder installed an Enricher via SetCalendar.
//
// v1: only the "ical" provider is wired. Other provider names in
// cfg.Providers are logged and skipped, so the config can name providers
// that will land in a later release without failing today's load.
func buildCalendarEnricher(cfg config.CalendarConfig) (calendar.Enricher, error) {
	if !cfg.Enabled {
		return nil, errors.New("calendar disabled")
	}

	var providers []icalendar.Provider
	for _, name := range cfg.Providers {
		switch name {
		case "ical":
			for _, entry := range cfg.ICal {
				p, err := ical.New(entry.Name, entry.URL, nil)
				if err != nil {
					return nil, fmt.Errorf("ical %q: %w", entry.Name, err)
				}
				providers = append(providers, p)
			}
		default:
			// Deferred providers (google, outlook). Not an error to name
			// them so config can be forward-compatible.
			fmt.Printf("calendar: provider %q not yet implemented; skipping\n", name)
		}
	}

	if len(providers) == 0 {
		return nil, errors.New("no calendar providers configured (populate [[calendar.ical]])")
	}

	enricher, err := icalendar.NewDefaultEnricher(cfg, providers)
	if err != nil {
		return nil, err
	}
	if cfg.Jev.Enabled {
		adj, err := icalendar.NewJevAdjudicator(cfg.Jev)
		if err != nil {
			fmt.Printf("Warning: jev adjudication disabled: %v\n", err)
		} else if adj != nil {
			enricher.SetJevAdjudicator(adj)
		}
	}
	return enricher, nil
}

// sessionWithCalendar embeds a session and adds the ephemeral calendar_event
// enrichment sourced from the local cache. It is the only place calendar_event
// ever appears in JSON output; session.json on disk is never touched by the
// calendar workstream.
type sessionWithCalendar struct {
	*session.Session
	CalendarEvent *calendar.Event `json:"calendar_event,omitempty"`
}

// withCalendar attaches the cached CalendarEvent (if any) to sess and returns
// the wrapped value. Safe to call with a nil calendarStore — returns the
// session with no CalendarEvent.
func (a *App) withCalendar(sess *session.Session) *sessionWithCalendar {
	out := &sessionWithCalendar{Session: sess}
	if a.calendarStore == nil || sess == nil {
		return out
	}
	m, err := a.calendarStore.Load(sess.ID)
	if err != nil {
		fmt.Printf("calendar cache: load %s: %v\n", sess.ID, err)
		return out
	}
	if m != nil && m.CalendarEvent != nil {
		out.CalendarEvent = m.CalendarEvent
	}
	return out
}

// withCalendarSlice wraps a slice of sessions with their cached calendar
// events.
func (a *App) withCalendarSlice(sessions []*session.Session) []*sessionWithCalendar {
	out := make([]*sessionWithCalendar, len(sessions))
	for i, s := range sessions {
		out[i] = a.withCalendar(s)
	}
	return out
}

// runCalendarEnrichment invokes the current enricher and writes the result to
// the ephemeral cache. Never touches session.json. Non-fatal on all error
// paths. Called from persistSession after diarize completes.
func (a *App) runCalendarEnrichment(sess *session.Session, windowTitle string) {
	if a.calendar == nil || a.calendarStore == nil || sess == nil {
		return
	}
	url := icalendar.ExtractMeetingURL(windowTitle)
	cached := &icalendar.CachedMatch{
		SessionID:   sess.ID,
		WindowTitle: windowTitle,
		MeetingURL:  url,
		UpdatedAt:   time.Now(),
	}
	in := calendar.MatchInput{
		SessionID:   sess.ID,
		StartTime:   sess.CreatedAt,
		EndTime:     sess.EndedAt,
		Platform:    sess.Platform,
		MeetingURL:  url,
		WindowTitle: windowTitle,
		Transcript:  flattenTranscript(sess),
	}
	if ev, err := a.calendar.Enrich(a.ctx, in); err != nil {
		fmt.Printf("Warning: calendar enrichment failed for %s: %v\n", sess.ID, err)
	} else if ev != nil {
		// Optional post-match participant enrichment. Errors are logged
		// and swallowed — the event keeps whatever the enricher produced.
		if a.calendarResolver != nil {
			if rerr := a.calendarResolver.ResolveParticipants(a.ctx, ev); rerr != nil {
				fmt.Printf("Warning: participant resolver failed for %s: %v\n", sess.ID, rerr)
			}
		}
		cached.CalendarEvent = ev
	}
	if err := a.calendarStore.Save(cached); err != nil {
		fmt.Printf("Warning: calendar cache save failed for %s: %v\n", sess.ID, err)
	}
}

// flattenTranscript renders a session's segments as a single plain-text
// string, in order, for adjudicators that decide on topic content.
// Speakers are prefixed so an LLM sees the turn boundaries.
func flattenTranscript(sess *session.Session) string {
	if sess == nil || len(sess.Segments) == 0 {
		return ""
	}
	var buf []byte
	for i, seg := range sess.Segments {
		if i > 0 {
			buf = append(buf, '\n')
		}
		if seg.Speaker != "" {
			buf = append(buf, seg.Speaker...)
			buf = append(buf, ':', ' ')
		}
		buf = append(buf, seg.Text...)
	}
	return string(buf)
}
