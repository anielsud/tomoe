// Package calendar implements Tomoe's default calendar Enricher, which
// composes one or more Providers (ICS URL subscriptions in v1) and scores
// their events against a session to pick the best match.
//
// The public seam is github.com/sosuke-ai/tomoe-pc/calendar. Nothing in this
// package is exported to consumers outside the module.
package calendar

import (
	"context"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

// Provider is the internal interface each source implements. Providers are
// listed inside a DefaultEnricher; they run in parallel per enrichment call
// and their results are pooled through the matcher.
//
// Provider is not exported. External embedders implement calendar.Enricher
// directly rather than plugging in Providers.
type Provider interface {
	// Name returns a short human-readable label (e.g. "Google ICS",
	// "Fastmail"). Surfaced on the resulting Event.
	Name() string

	// ListEvents returns every event whose start time falls within
	// [from, to]. Implementations may go slightly outside the window
	// (e.g. a recurring event's first instance before `from`) but should
	// not return events millions of years away.
	ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error)
}
