package calendar

import "time"

// Event is a calendar event linked to a recorded session. It is populated by
// an Enricher (see calendar.go) and surfaced to Wails clients as an optional
// field on the Session response. It is not persisted to session.json.
//
// The zero value is not a valid event; check for a non-nil pointer at the
// consumer.
type Event struct {
	// Source is a short identifier for the origin of the event, e.g.
	// "ical", "google", "outlook", or any string an external embedder
	// chooses. It's informational — not used by the matcher.
	Source string `json:"source"`

	// Provider is a human-readable name for the specific provider that
	// yielded this event, e.g. "Google Calendar", "Fastmail ICS",
	// "Personal ICS".
	Provider string `json:"provider"`

	// EventID is a stable identifier from the source, when available. Used
	// for equality across re-enrichment passes.
	EventID string `json:"event_id,omitempty"`

	Title        string        `json:"title"`
	Organizer    *Participant  `json:"organizer,omitempty"`
	Participants []Participant `json:"participants,omitempty"`

	// ParticipantCount is authoritative even when Participants is truncated
	// or omitted by the source (some ICS feeds strip ATTENDEE lines).
	ParticipantCount int `json:"participant_count,omitempty"`

	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	MeetingURL string    `json:"meeting_url,omitempty"`

	// MatchedAt records when enrichment produced this event.
	MatchedAt time.Time `json:"matched_at"`

	// MatchScore is the 0–100 score the matcher produced. Retained for
	// debugging; not surfaced by default in the frontend.
	MatchScore int `json:"match_score,omitempty"`
}

// Participant is one attendee on an Event. Fields are optional because
// different providers surface different subsets (ICS often has only a name
// and email; other sources may include response status).
type Participant struct {
	Name           string `json:"name,omitempty"`
	Email          string `json:"email,omitempty"`
	ResponseStatus string `json:"response_status,omitempty"` // "accepted" | "declined" | "tentative" | "needsAction" | ""
	IsOrganizer    bool   `json:"is_organizer,omitempty"`
}
