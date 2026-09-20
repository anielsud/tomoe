// Package ical implements Tomoe's ICS URL subscription provider.
//
// Each configured [[calendar.ical]] block gets one Provider instance. The
// enricher fans out to all instances in parallel per enrichment call.
//
// v1 limitation: recurring events (RRULE) are treated as one-shot instances
// at their base DTSTART. Feeds that materialize instances (Google Calendar's
// private ICS URL does) work fine; feeds that only send RRULE will miss
// subsequent instances until we adopt an RRULE expander.
package ical

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	icalendar "github.com/sosuke-ai/tomoe-pc/internal/calendar"
)

// defaultHTTPTimeout bounds the whole ICS fetch. Publisher CDNs are usually
// fast; a slow feed is likely a misconfiguration the user wants to know
// about immediately, not a hang.
const defaultHTTPTimeout = 15 * time.Second

// Provider is one ICS URL subscription. Safe for concurrent ListEvents
// calls provided the *http.Client is safe (net/http's default Client is).
type Provider struct {
	name   string
	url    string
	client *http.Client
}

// New constructs a Provider for one ICS URL. Pass nil for client to use a
// sensible default with a 15-second timeout.
func New(name, url string, client *http.Client) (*Provider, error) {
	if name == "" {
		return nil, errors.New("ical: empty provider name")
	}
	if url == "" {
		return nil, errors.New("ical: empty URL")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Provider{name: name, url: url, client: client}, nil
}

// Name implements internal calendar.Provider.
func (p *Provider) Name() string { return p.name }

// ListEvents fetches the ICS feed and returns events whose start time falls
// within [from, to].
func (p *Provider) ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("ical %q: build request: %w", p.name, err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ical %q: GET: %w", p.name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ical %q: GET returned %s", p.name, resp.Status)
	}
	return parseICS(p.name, resp.Body, from, to)
}

// parseICS reads an ICS document and returns the events whose start falls
// within [from, to]. Exposed to package-internal tests via fixture files.
//
// Buffers the whole document so Windows-style TZIDs (Outlook / Exchange)
// can be rewritten to IANA before golang-ical parses them. See wintz.go.
func parseICS(providerName string, r io.Reader, from, to time.Time) ([]calendar.Event, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("ical %q: read: %w", providerName, err)
	}
	body = rewriteWindowsTZIDs(body)

	cal, err := ics.ParseCalendar(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ical %q: parse: %w", providerName, err)
	}
	var out []calendar.Event
	for _, evt := range cal.Events() {
		start, err := evt.GetStartAt()
		if err != nil {
			// Skip malformed events rather than fail the whole feed.
			continue
		}
		end, err := evt.GetEndAt()
		if err != nil {
			// If DTEND is missing, treat as a 30-minute default.
			end = start.Add(30 * time.Minute)
		}
		if start.Before(from) || start.After(to) {
			continue
		}
		out = append(out, convertEvent(providerName, evt, start, end))
	}
	return out, nil
}

func convertEvent(providerName string, evt *ics.VEvent, start, end time.Time) calendar.Event {
	e := calendar.Event{
		Source:    "ical",
		Provider:  providerName,
		EventID:   evt.Id(),
		Title:     propValue(evt, ics.ComponentPropertySummary),
		StartTime: start,
		EndTime:   end,
	}

	description := propValue(evt, ics.ComponentPropertyDescription)
	location := propValue(evt, ics.ComponentPropertyLocation)

	e.MeetingURL = icalendar.ExtractMeetingURL(location + " " + description)
	if e.MeetingURL == "" {
		// Fall back to LOCATION if it looks like a URL even without a
		// recognized platform host.
		if isURL(location) {
			e.MeetingURL = location
		}
	}

	if org := evt.GetProperty(ics.ComponentPropertyOrganizer); org != nil {
		p := convertOrganizer(org.BaseProperty)
		p.IsOrganizer = true
		e.Organizer = &p
	}

	for _, a := range evt.Attendees() {
		e.Participants = append(e.Participants, convertAttendee(a))
	}
	// Some feeds strip ATTENDEE lines for privacy; ParticipantCount stays
	// authoritative even when Participants is short.
	e.ParticipantCount = len(e.Participants)
	if e.ParticipantCount == 0 && e.Organizer != nil {
		e.ParticipantCount = 1
	}
	return e
}

func propValue(evt *ics.VEvent, name ics.ComponentProperty) string {
	if p := evt.GetProperty(name); p != nil {
		return p.Value
	}
	return ""
}

func convertOrganizer(bp ics.BaseProperty) calendar.Participant {
	p := calendar.Participant{Email: stripMailto(bp.Value)}
	if cn := paramValue(bp.ICalParameters, "CN"); cn != "" {
		p.Name = cn
	}
	return p
}

func convertAttendee(a *ics.Attendee) calendar.Participant {
	p := calendar.Participant{Email: stripMailto(a.Value)}
	if cn := paramValue(a.ICalParameters, "CN"); cn != "" {
		p.Name = cn
	}
	if status := paramValue(a.ICalParameters, "PARTSTAT"); status != "" {
		p.ResponseStatus = normalizeStatus(status)
	}
	if role := paramValue(a.ICalParameters, "ROLE"); role == "CHAIR" {
		p.IsOrganizer = true
	}
	return p
}

func stripMailto(v string) string {
	return strings.TrimPrefix(v, "mailto:")
}

func paramValue(params map[string][]string, key string) string {
	if v, ok := params[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

// normalizeStatus maps ICS PARTSTAT values to the frontend's expected form.
func normalizeStatus(s string) string {
	switch strings.ToUpper(s) {
	case "ACCEPTED":
		return "accepted"
	case "DECLINED":
		return "declined"
	case "TENTATIVE":
		return "tentative"
	case "NEEDS-ACTION":
		return "needsAction"
	default:
		return strings.ToLower(s)
	}
}

func isURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Compile-time assertion that Provider satisfies the internal Provider
// interface.
var _ icalendar.Provider = (*Provider)(nil)
