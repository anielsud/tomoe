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

// ParticipantResolver is an optional post-match hook that can enrich, replace,
// or normalize the participant list on an Event before it is cached and
// surfaced to the frontend. It is called by the backend after the Enricher
// returns a non-nil match, orthogonally to how the match was made.
//
// Typical use cases: overlay canonical identity (photo, employee id,
// preferred name) from an internal directory; deduplicate an ICS
// ATTENDEE list against an HR system; hide external attendees from the
// UI; join with a CRM contact record.
//
// The resolver mutates ev in place — usually by reassigning ev.Participants
// and/or ev.Organizer. It runs on the same serial saveWorker goroutine as
// the enricher, so implementations do not need to be reentrant, but they
// must be safe to call concurrently across independent App instances.
//
// Errors are logged by the caller and treated as no-op: the event keeps
// whatever the Enricher produced. A resolver never blocks session save.
type ParticipantResolver interface {
	ResolveParticipants(ctx context.Context, ev *Event) error
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
	// Transcript is the session's flattened text, useful for enrichers that
	// disambiguate via topic content (e.g. an LLM adjudicator). Populated
	// by Tomoe from the session's segments in order; the enricher decides
	// how much to consume. Empty when the transcript is not yet available.
	Transcript string
}
