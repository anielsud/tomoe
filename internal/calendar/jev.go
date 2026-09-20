package calendar

import (
	"context"
	"errors"
	"fmt"

	"github.com/imyousuf/CodeEagle/pkg/jev"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// choiceKeys used for the topic-match question.
const (
	choiceYes       = "yes"
	choiceAmbiguous = "ambiguous"
	choiceNo        = "no"
)

// minMargin is the minimum probability gap between the top and second Ranked
// options required to trust the answer. Jev responses are not bit-stable
// (README documents ±0.01–0.09 variance between identical requests) so a
// close margin means "actually ambiguous" regardless of which key won.
const defaultMinMargin = 0.15

// bytesPerSecondEstimate is a rough proxy: transcripts flatten to plain
// text at roughly this many bytes per second of speech (English, natural
// pace). Used to bound the transcript slice a JevAdjudicator sends up.
const bytesPerSecondEstimate = 15

// JevAdjudicator asks Jev to decide whether a session's transcript
// corresponds to a candidate calendar event. Returns a bounded points
// contribution the matcher folds into its total score.
//
// JevAdjudicator is opt-in — construct one only when config.JevConfig has
// Enabled=true. When disabled the enricher's Jev slot stays nil and no
// calls happen.
type JevAdjudicator struct {
	asker     jev.Asker
	weight    int
	ctxSecs   int
	minMargin float64
}

// NewJevAdjudicator builds an adjudicator from JevConfig. Returns (nil, nil)
// when JevConfig.Enabled is false, so callers can inline the result and
// pass nil into DefaultEnricher's config.
func NewJevAdjudicator(cfg config.JevConfig) (*JevAdjudicator, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.APIKey == "" {
		return nil, errors.New("jev: [calendar.jev].enabled is true but api_key is empty")
	}
	var opts []jev.Option
	if cfg.Model != "" {
		opts = append(opts, jev.WithModel(cfg.Model))
	}
	client, err := jev.New(cfg.APIKey, opts...)
	if err != nil {
		return nil, fmt.Errorf("jev: %w", err)
	}
	return newJevAdjudicator(client, cfg), nil
}

// newJevAdjudicator is the test-friendly entry: takes any jev.Asker.
func newJevAdjudicator(asker jev.Asker, cfg config.JevConfig) *JevAdjudicator {
	weight := cfg.TopicWeight
	if weight <= 0 {
		weight = 40
	}
	secs := cfg.TranscriptContextSeconds
	if secs <= 0 {
		secs = 120
	}
	return &JevAdjudicator{
		asker:     asker,
		weight:    weight,
		ctxSecs:   secs,
		minMargin: defaultMinMargin,
	}
}

// Score returns the points contribution and Jev's choice for candidate.
// Non-fatal on any failure — caller should treat errors as "no signal".
//
// Contribution:
//   - "yes" with margin      →  (weight, "yes", nil)
//   - "ambiguous" with margin → (weight/2, "ambiguous", nil)
//   - "no" with margin        → (0, "no", nil)
//   - below margin            → (0, "", nil)
//   - error                   → (0, "", err)
func (j *JevAdjudicator) Score(ctx context.Context, transcript string, candidate calendar.Event) (int, string, error) {
	if j == nil || j.asker == nil {
		return 0, "", nil
	}
	trimmed := trimTranscript(transcript, j.ctxSecs)

	state := map[string]any{
		"transcript":        trimmed,
		"event_title":       candidate.Title,
		"event_description": "",
		"attendees":         summarizeAttendees(candidate),
	}
	questions := jev.Questions{
		"match": jev.Choice(
			"Does this transcript match this scheduled calendar event? "+
				"'yes' if the transcript is clearly about this event's topic and attendees; "+
				"'no' if it clearly is not; 'ambiguous' if you cannot tell.",
			map[string]string{
				choiceYes:       "The transcript's topic and participants match the event.",
				choiceAmbiguous: "The transcript is short, off-topic, or lacks signal to decide.",
				choiceNo:        "The transcript is clearly about a different meeting.",
			},
		),
	}
	resp, err := j.asker.Ask(ctx, state, questions)
	if err != nil {
		return 0, "", err
	}
	ranked, err := resp.Answers.Ranked("match")
	if err != nil || len(ranked) == 0 {
		return 0, "", err
	}
	top := ranked[0]
	margin := top.Probability
	if len(ranked) > 1 {
		margin -= ranked[1].Probability
	}
	if margin < j.minMargin {
		return 0, "", nil
	}
	switch top.Option {
	case choiceYes:
		return j.weight, choiceYes, nil
	case choiceAmbiguous:
		return j.weight / 2, choiceAmbiguous, nil
	case choiceNo:
		return 0, choiceNo, nil
	default:
		return 0, "", nil
	}
}

// TieBreak outcomes for the enricher.
const (
	// TieBreakUndecided means Jev's answer was below the confidence
	// margin. The caller should leave the heuristic result alone.
	TieBreakUndecided = -1
	// TieBreakNone means Jev confidently picked "none of the above". The
	// caller should suppress every candidate's yes-boost — the transcript
	// does not match any of them.
	TieBreakNone = -2
)

// tieBreakOptionCap bounds how many candidates one TieBreak call presents
// to Jev at once. Jev's Choice primitive tops out at ~10 options; +1 for
// "none" leaves room for 9 candidates. In practice the caller only reaches
// TieBreak with 2–3 concurrent meetings, so the cap is a safety belt.
const tieBreakOptionCap = 8

// TieBreak picks which of several candidates the transcript actually
// corresponds to when the per-candidate Score returned "yes" for more than
// one. Returns:
//   - i in [0, len(candidates)) — that candidate wins.
//   - TieBreakNone              — Jev confidently says none of them match.
//   - TieBreakUndecided         — margin insufficient; caller decides.
//   - (?, err)                  — API failure; caller decides.
//
// candidates order matches the returned index. Extra candidates beyond
// tieBreakOptionCap are dropped from the ask.
func (j *JevAdjudicator) TieBreak(ctx context.Context, transcript string, candidates []calendar.Event) (int, error) {
	if j == nil || j.asker == nil || len(candidates) < 2 {
		return TieBreakUndecided, nil
	}
	presented := candidates
	if len(presented) > tieBreakOptionCap {
		presented = presented[:tieBreakOptionCap]
	}

	options := make(map[string]string, len(presented)+1)
	summaries := make(map[string]string, len(presented))
	for i, ev := range presented {
		key := fmt.Sprintf("event_%d", i)
		options[key] = tieBreakOptionLabel(ev)
		summaries[key] = ev.Title
	}
	options[choiceNone] = "None of the listed events matches this transcript."

	trimmed := trimTranscript(transcript, j.ctxSecs)
	state := map[string]any{
		"transcript": trimmed,
		"candidates": summaries,
	}
	questions := jev.Questions{
		"which": jev.Choice(
			"Multiple scheduled events overlap this transcript. Which one is the "+
				"transcript actually about? Pick 'none' if you cannot tell which meeting was attended.",
			options,
		),
	}
	resp, err := j.asker.Ask(ctx, state, questions)
	if err != nil {
		return TieBreakUndecided, err
	}
	ranked, err := resp.Answers.Ranked("which")
	if err != nil || len(ranked) == 0 {
		return TieBreakUndecided, err
	}
	top := ranked[0]
	margin := top.Probability
	if len(ranked) > 1 {
		margin -= ranked[1].Probability
	}
	if margin < j.minMargin {
		return TieBreakUndecided, nil
	}
	if top.Option == choiceNone {
		return TieBreakNone, nil
	}
	// Parse event_N.
	var idx int
	if _, err := fmt.Sscanf(top.Option, "event_%d", &idx); err != nil {
		return TieBreakUndecided, nil
	}
	if idx < 0 || idx >= len(presented) {
		return TieBreakUndecided, nil
	}
	return idx, nil
}

// choiceNone is the sentinel for "no candidate matches", added to every
// tie-break question so Jev can honestly decline to pick.
const choiceNone = "none"

// tieBreakOptionLabel renders a compact one-line description of a candidate
// for Jev's Choice options.
func tieBreakOptionLabel(ev calendar.Event) string {
	if ev.Title == "" {
		return "Untitled event"
	}
	if !ev.StartTime.IsZero() {
		return fmt.Sprintf("%s (starts %s)", ev.Title, ev.StartTime.Format("15:04 MST"))
	}
	return ev.Title
}

// trimTranscript returns the first ctxSecs seconds' worth of transcript,
// approximated by byte count. English natural-pace speech flattens to
// ~bytesPerSecondEstimate bytes/sec of transcript.
func trimTranscript(transcript string, ctxSecs int) string {
	if ctxSecs <= 0 {
		return transcript
	}
	limit := ctxSecs * bytesPerSecondEstimate
	if len(transcript) <= limit {
		return transcript
	}
	// Cut on a rune boundary and, ideally, on whitespace so we do not send
	// a truncated word.
	cut := limit
	for cut > 0 && !isSpace(transcript[cut]) {
		cut--
	}
	if cut == 0 {
		cut = limit
	}
	return transcript[:cut]
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\n', '\t', '\r':
		return true
	}
	return false
}

// summarizeAttendees renders a compact string form of the event's known
// participants for the Jev state payload. Kept short — Jev bills by input
// tokens.
func summarizeAttendees(candidate calendar.Event) string {
	if candidate.ParticipantCount == 0 && candidate.Organizer == nil {
		return "unknown"
	}
	out := ""
	if candidate.Organizer != nil {
		if candidate.Organizer.Name != "" {
			out += candidate.Organizer.Name + " (organizer)"
		} else if candidate.Organizer.Email != "" {
			out += candidate.Organizer.Email + " (organizer)"
		}
	}
	count := 0
	for _, p := range candidate.Participants {
		if count >= 5 {
			break
		}
		if p.IsOrganizer {
			continue
		}
		label := p.Name
		if label == "" {
			label = p.Email
		}
		if label == "" {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += label
		count++
	}
	if candidate.ParticipantCount > count+1 {
		remaining := candidate.ParticipantCount - count - 1
		out += fmt.Sprintf(" +%d more", remaining)
	}
	return out
}
