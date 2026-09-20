// Package calendar defines the public seam Tomoe exposes for calendar-event
// enrichment of recorded sessions.
//
// Tomoe ships a default Enricher (in internal/calendar) that reads ICS URL
// subscriptions. External projects that embed Tomoe can supply their own
// Enricher — for a directory service, an OAuth provider, an in-house
// scheduling system — by implementing this interface and wiring it via
// (*backend.App).SetCalendar.
//
// The interface is deliberately narrow: one method, a value type in, a value
// type out. Additional fields on Event, Participant, and MatchInput may be
// added over time; existing fields will not change semantics without a major
// version bump.
package calendar

import (
	"context"
	"time"
)

// Enricher resolves a Tomoe session to a calendar event, or returns nil when
// no match is found. Implementations must be safe for concurrent use;
// enrichment runs on Tomoe's serial saveWorker goroutine but a single
// enrichment call may fan out to multiple providers concurrently.
//
// Errors are logged by the caller and treated as "no match" — enrichment
// failure never blocks session save.
type Enricher interface {
	Enrich(ctx context.Context, in MatchInput) (*Event, error)
}

// MatchInput is the stable set of facts about a just-recorded session that
// Tomoe hands to the Enricher. Fields may be zero-valued (empty string, zero
// time) when Tomoe could not determine them for a particular session; the
// Enricher must tolerate that.
//
// The zero value of MatchInput is safe to pass but not useful — an Enricher
// receiving it should return (nil, nil).
type MatchInput struct {
	SessionID   string
	StartTime   time.Time
	EndTime     time.Time
	Platform    string // "Teams", "Meet", "Zoom", "Webex", "Slack", or ""
	MeetingURL  string // extracted from the window title when available; may be ""
	WindowTitle string // raw, when captured; may be ""
}
