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
	// Warnings are lines that probably didn't parse as intended, for the
	// person who edited the transcript to check.
	Warnings []string
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
// or more spaces, then m:ss or h:mm:ss. looseHeader allows a single space,
// as hand edits often have; it's only trusted for a name some strict header
// already uses, so a text line like "we meet at 3:00" isn't mistaken for
// a speaker.
var (
	turnHeader  = regexp.MustCompile(`^(\S.*?)\s{2,}(\d+(?::\d{2}){1,2})\s*$`)
	looseHeader = regexp.MustCompile(`^(\S.*?)\s+(\d+(?::\d{2}){1,2})\s*$`)
)

// ParseTeamsTranscript parses a Microsoft Teams transcript exported as text:
// a few header lines (title, host, date, length), then blocks of
// "Speaker Name  m:ss" followed by the turn's text. mediaEnd (seconds) ends
// the last turn; pass 0 to end it at its own start plus a rough estimate
// from its word count.
//
// Turns that share a start time are simultaneous speech: a reviewer marks
// an interjection by splitting the main speaker's turn and giving both
// parts the same time. See setEnds for how their ends are set.
func ParseTeamsTranscript(r io.Reader, mediaEnd float64) (*Reference, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty transcript")
	}

	known := map[string]bool{}
	for _, line := range lines[1:] {
		if m := turnHeader.FindStringSubmatch(line); m != nil {
			known[strings.TrimSpace(m[1])] = true
		}
	}

	ref := &Reference{Title: strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff"))}
	var cur *Turn
	var text []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(strings.Join(text, " "))
			ref.Turns = append(ref.Turns, *cur)
		}
		cur, text = nil, nil
	}
	for n, line := range lines[1:] {
		lineNo := n + 2
		m := turnHeader.FindStringSubmatch(line)
		if m == nil {
			if lm := looseHeader.FindStringSubmatch(line); lm != nil {
				if known[strings.TrimSpace(lm[1])] {
					m = lm
				} else if cur != nil {
					ref.Warnings = append(ref.Warnings, fmt.Sprintf("line %d: %q looks like a speaker line but %q isn't a known speaker, so it's read as text", lineNo, line, strings.TrimSpace(lm[1])))
				}
			}
		}
		if m != nil {
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
	flush()
	if len(ref.Turns) == 0 {
		return nil, fmt.Errorf("no speaker turns found (expected lines like \"Name  1:23\")")
	}
	for i := 1; i < len(ref.Turns); i++ {
		if ref.Turns[i].Start < ref.Turns[i-1].Start {
			ref.Warnings = append(ref.Warnings, fmt.Sprintf("turn %d (%s at %s) starts before the turn above it", i+1, ref.Turns[i].Speaker, clock(ref.Turns[i].Start)))
		}
	}
	setEnds(ref.Turns, mediaEnd)
	return ref, nil
}

// estimatedLength is how long a turn's words take to say: ~2.5 words per
// second, plus a little slack.
func estimatedLength(text string) float64 {
	return float64(len(Words(text)))/2.5 + 1
}

// setEnds gives each turn an end: the next turn that starts later (or the
// media end). Turns sharing a start time overlap: the longest one (most
// words, the main speaker carrying on) runs to that next start, and the
// others (interjections) end after their own estimated length, so an
// interjection doesn't cover the rest of the other person's turn.
func setEnds(turns []Turn, mediaEnd float64) {
	for i := 0; i < len(turns); {
		j := i
		for j < len(turns) && turns[j].Start == turns[i].Start {
			j++
		}
		next := mediaEnd
		if j < len(turns) {
			next = turns[j].Start
		}
		longest := i
		for k := i; k < j; k++ {
			if len(Words(turns[k].Text)) > len(Words(turns[longest].Text)) {
				longest = k
			}
		}
		for k := i; k < j; k++ {
			end := next
			if k != longest {
				end = min(next, turns[k].Start+estimatedLength(turns[k].Text))
			}
			if end <= turns[k].Start { // last turn, with no media end given
				end = turns[k].Start + estimatedLength(turns[k].Text)
			}
			turns[k].End = end
		}
		i = j
	}
}

// Overlapping reports whether turn i shares its start time with another
// turn: annotated simultaneous speech.
func (r *Reference) Overlapping(i int) bool {
	t := r.Turns[i]
	return (i > 0 && r.Turns[i-1].Start == t.Start) || (i+1 < len(r.Turns) && r.Turns[i+1].Start == t.Start)
}

func clock(sec float64) string {
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
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

// Slice returns the turns starting within [from, to) seconds, shifted so
// from becomes 0 and clipped to end by to, for scoring one stretch of a
// recording (see `tomoe eval --from/--to`).
func (r *Reference) Slice(from, to float64) *Reference {
	out := &Reference{Title: r.Title}
	for _, t := range r.Turns {
		if t.Start < from || t.Start >= to {
			continue
		}
		t.Start -= from
		t.End = min(t.End, to) - from
		out.Turns = append(out.Turns, t)
	}
	return out
}
