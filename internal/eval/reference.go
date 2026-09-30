// Package eval scores Tomoe's transcription and speaker-labeling passes
// against a reference transcript, one score per pass and problem area (see
// `tomoe eval`). Everything here is pure Go and model-free, so the scoring
// itself is unit-tested independently of the pipeline being scored.
package eval

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Turn is one speaker turn in a reference transcript.
type Turn struct {
	Speaker string
	Start   float64 // seconds
	// End is the next turn's start (or the media end for the last turn):
	// a Teams export only gives start times, so a turn's end includes any
	// pause before the next one. Speaker scoring only counts time where the
	// pipeline detected speech, so the pause doesn't count against it.
	End  float64
	Text string
}

// Reference is a parsed reference transcript.
type Reference struct {
	Title string
	Turns []Turn
}

// Speakers returns the distinct speakers in first-appearance order.
func (r *Reference) Speakers() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range r.Turns {
		if !seen[t.Speaker] {
			seen[t.Speaker] = true
			out = append(out, t.Speaker)
		}
	}
	return out
}

// turnHeader matches a Teams transcript turn header: the speaker name, two
// or more spaces, then m:ss or h:mm:ss.
var turnHeader = regexp.MustCompile(`^(\S.*?)\s{2,}(\d+(?::\d{2}){1,2})\s*$`)

// ParseTeamsTranscript parses a Microsoft Teams transcript exported as text:
// a few header lines (title, host, date, length), then blocks of
// "Speaker Name  m:ss" followed by the turn's text. mediaEnd (seconds) ends
// the last turn; pass 0 to end it at its own start plus a rough estimate
// from its word count.
func ParseTeamsTranscript(r io.Reader, mediaEnd float64) (*Reference, error) {
	ref := &Reference{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var cur *Turn
	var text []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(strings.Join(text, " "))
			ref.Turns = append(ref.Turns, *cur)
		}
		cur, text = nil, nil
	}
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		if lineNo == 1 {
			ref.Title = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
			continue
		}
		if m := turnHeader.FindStringSubmatch(line); m != nil {
			start, err := parseClock(m[2])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			flush()
			cur = &Turn{Speaker: strings.TrimSpace(m[1]), Start: start}
			continue
		}
		if cur != nil && strings.TrimSpace(line) != "" {
			text = append(text, strings.TrimSpace(line))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	if len(ref.Turns) == 0 {
		return nil, fmt.Errorf("no speaker turns found (expected lines like \"Name  1:23\")")
	}
	for i := range ref.Turns {
		switch {
		case i+1 < len(ref.Turns):
			ref.Turns[i].End = ref.Turns[i+1].Start
		case mediaEnd > ref.Turns[i].Start:
			ref.Turns[i].End = mediaEnd
		default:
			// ~2.5 words per second of speech, plus a little slack.
			ref.Turns[i].End = ref.Turns[i].Start + float64(len(Words(ref.Turns[i].Text)))/2.5 + 1
		}
	}
	return ref, nil
}

// parseClock parses m:ss or h:mm:ss into seconds.
func parseClock(s string) (float64, error) {
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("bad timestamp %q", s)
		}
		total = total*60 + n
	}
	return float64(total), nil
}
