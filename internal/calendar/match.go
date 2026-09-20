package calendar

import (
	"regexp"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

// scoringConfig captures the tunables passed from CalendarConfig into the
// matcher without dragging the whole config package as a dependency.
type scoringConfig struct {
	startWindow    time.Duration
	endBounded     bool
	scoreThreshold int
}

// score returns a 0-100 score for candidate against the session inputs.
// See docs/calendar-integration-tech-brief.md §6.5 for the rationale.
//
//	Start-time alignment: up to 30 (linear falloff from 0 lag to startWindow)
//	Meeting URL match:    20 (all-or-nothing when a URL substring matches)
//	Platform hint:        10 (Meet/Teams/Zoom host detected in DESCRIPTION/LOCATION)
//
// Jev topic adjudication (Phase 2) adds up to 40 more on top when enabled.
func score(candidate calendar.Event, in calendar.MatchInput, cfg scoringConfig) int {
	total := 0
	total += scoreStartAlignment(candidate.StartTime, in.StartTime, cfg.startWindow)
	total += scoreURL(candidate, in.MeetingURL)
	total += scorePlatformHint(candidate, in.Platform)
	if total < 0 {
		total = 0
	}
	if total > 100 {
		total = 100
	}
	return total
}

func scoreStartAlignment(candidateStart, sessionStart time.Time, window time.Duration) int {
	if candidateStart.IsZero() || sessionStart.IsZero() || window <= 0 {
		return 0
	}
	lag := sessionStart.Sub(candidateStart)
	if lag < 0 {
		lag = -lag
	}
	if lag > window {
		return 0
	}
	// Linear falloff: 30 at 0 lag, 0 at window.
	ratio := float64(window-lag) / float64(window)
	return int(30 * ratio)
}

func scoreURL(candidate calendar.Event, sessionURL string) int {
	if sessionURL == "" {
		return 0
	}
	// The candidate's URL is populated by the provider (from LOCATION or
	// DESCRIPTION when a join URL is detectable). Substring match on
	// either direction covers "sessionURL is in the event body" and vice
	// versa.
	if candidate.MeetingURL != "" &&
		(strings.Contains(candidate.MeetingURL, sessionURL) ||
			strings.Contains(sessionURL, candidate.MeetingURL)) {
		return 20
	}
	return 0
}

// platformHosts maps Tomoe's detected platform strings to a URL host
// fragment that would appear inside an event body if the meeting is on
// that platform.
var platformHosts = map[string]string{
	"Teams": "teams.microsoft.com",
	"Meet":  "meet.google.com",
	"Zoom":  "zoom.us",
	"Webex": "webex.com",
	"Slack": "slack.com",
}

func scorePlatformHint(candidate calendar.Event, platform string) int {
	if platform == "" {
		return 0
	}
	host, ok := platformHosts[platform]
	if !ok {
		return 0
	}
	haystack := strings.ToLower(candidate.MeetingURL + " " + candidate.Title)
	if strings.Contains(haystack, host) {
		return 10
	}
	return 0
}

// pickBest chooses the best candidate above threshold, with the tie-break
// rules from §6.5: highest score wins; on a tie, the shortest event wins.
// Returns nil when nothing clears the threshold.
func pickBest(candidates []scoredEvent, threshold int) *scoredEvent {
	var best *scoredEvent
	for i := range candidates {
		c := &candidates[i]
		if c.total < threshold {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		if c.total > best.total {
			best = c
			continue
		}
		if c.total == best.total {
			cDur := c.event.EndTime.Sub(c.event.StartTime)
			bDur := best.event.EndTime.Sub(best.event.StartTime)
			if cDur > 0 && (bDur <= 0 || cDur < bDur) {
				best = c
			}
		}
	}
	return best
}

// scoredEvent pairs an event with its heuristic score and the source
// Provider name (used for logs and to set Event.Provider).
type scoredEvent struct {
	event    calendar.Event
	total    int
	provider string
}

// candidateWindow computes the [from, to] window a provider should list
// events in for a given session, given the configured start-window and
// end-window-bound settings.
func candidateWindow(sessionStart time.Time, startWindow time.Duration, endBounded bool) (from, to time.Time) {
	from = sessionStart.Add(-startWindow)
	if endBounded {
		to = sessionStart.Add(startWindow).Add(2 * time.Hour) // typical max meeting run-over
	} else {
		to = sessionStart.Add(24 * time.Hour) // effectively unbounded for one session
	}
	return from, to
}

// meetingURLPatterns catches known join URLs in a window title. Extracted
// from the WindowTitle populated by MeetingEvent (X11 only, for now).
var meetingURLPatterns = []*regexp.Regexp{
	regexp.MustCompile(`https?://meet\.google\.com/[a-zA-Z0-9\-]+`),
	regexp.MustCompile(`https?://[a-zA-Z0-9\-]*\.?zoom\.us/j/[0-9]+(?:\?pwd=[^\s"<>]*)?`),
	regexp.MustCompile(`https?://teams\.microsoft\.com/l/meetup-join/[^\s"<>]+`),
	regexp.MustCompile(`https?://[a-zA-Z0-9\-]*\.?webex\.com/(?:meet|j)/[^\s"<>]+`),
}

// ExtractMeetingURL returns the first join URL found in title, or "" if
// no pattern matches. Exported so the backend enrichment path can call it
// during persistSession.
func ExtractMeetingURL(title string) string {
	for _, re := range meetingURLPatterns {
		if m := re.FindString(title); m != "" {
			return m
		}
	}
	return ""
}
