package session

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// teamsFileName is the Microsoft Teams transcript of the meeting a session
// recorded, written into the session's folder by a companion app. Tomoe
// only reads it: it never writes, changes or deletes it.
const teamsFileName = "teams.json"

// teamsWriter and teamsSource are the only writer and source whose
// teams.json is trusted (the same writer as calendar.json).
const (
	teamsWriter = calendarWriter
	teamsSource = "teams"
)

// Bounds on what is read from teams.json. A long meeting's transcript is
// a few hundred kilobytes and a few thousand turns.
const (
	maxTeamsFileBytes = 16 << 20
	maxTeamsTurns     = 100000
	maxTeamsSpeaker   = 200 // runes
	maxTeamsSeconds   = 24 * 3600
)

// TeamsTurn is one speaker turn of a Teams transcript. Offset is seconds
// from the transcript's Start. A turn has no end of its own: it lasts
// until the next turn starts (see TeamsTranscript.Spans).
type TeamsTurn struct {
	Speaker string  `json:"speaker"`
	Offset  float64 `json:"offset"`
	Text    string  `json:"text"`
}

// TeamsTranscript is a session's teams.json: Teams names every speaker.
type TeamsTranscript struct {
	Writer  string      `json:"writer"`
	Source  string      `json:"source"`
	Subject string      `json:"subject"`
	Start   time.Time   `json:"start"` // when Teams transcription started
	End     time.Time   `json:"end"`
	Turns   []TeamsTurn `json:"turns"`
}

// TeamsTranscript is the Teams transcript of the session with this id
// (its folder's teams.json, written by the companion app), or nil when
// there is no such file, it is unreadable, from another writer or source,
// or has no usable turns.
func (s *Store) TeamsTranscript(id string) *TeamsTranscript {
	if id == "" || id != filepath.Base(id) {
		return nil // a session ID is a single folder name
	}
	return ReadTeamsTranscript(filepath.Join(s.baseDir, id))
}

// ReadTeamsTranscript reads teams.json in a session folder; see
// Store.TeamsTranscript. Turns with no speaker or an impossible offset
// are dropped, speaker names are made one printable line, and the turns
// are put in time order.
func ReadTeamsTranscript(dir string) *TeamsTranscript {
	f, err := os.Open(filepath.Join(dir, teamsFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTeamsFileBytes+1))
	if err != nil || len(data) > maxTeamsFileBytes {
		return nil
	}
	var t TeamsTranscript
	if json.Unmarshal(data, &t) != nil || t.Writer != teamsWriter || t.Source != teamsSource || t.Start.IsZero() {
		return nil
	}
	if len(t.Turns) > maxTeamsTurns {
		return nil
	}
	turns := t.Turns[:0]
	for _, tr := range t.Turns {
		tr.Speaker = cleanName(tr.Speaker, maxTeamsSpeaker)
		if tr.Speaker == "" || math.IsNaN(tr.Offset) || tr.Offset < 0 || tr.Offset > maxTeamsSeconds {
			continue
		}
		turns = append(turns, tr)
	}
	if len(turns) == 0 {
		return nil
	}
	sortTurns(turns)
	t.Turns = turns
	t.Subject = cleanName(t.Subject, maxCalendarSubject)
	return &t
}

// sortTurns orders turns by offset, keeping the file's order for ties.
func sortTurns(turns []TeamsTurn) {
	for i := 1; i < len(turns); i++ {
		for j := i; j > 0 && turns[j].Offset < turns[j-1].Offset; j-- {
			turns[j], turns[j-1] = turns[j-1], turns[j]
		}
	}
}

// cleanName makes s one line of printable text, at most limit runes: a
// name from another program ends up in session.json and on a terminal.
func cleanName(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		s = strings.TrimSpace(string(r[:limit]))
	}
	return s
}

// maxTeamsTurnSeconds caps how long a turn is taken to last when the next
// one is far off (a pause, or the end of the transcript).
const maxTeamsTurnSeconds = 60

// TeamsSpan is a Teams turn placed on a session's clock (seconds from the
// session's CreatedAt, before any shift).
type TeamsSpan struct {
	Speaker    string
	Start, End float64
	Text       string
}

// Spans places the transcript's turns on the clock of a session created at
// created: each turn lasts until the next turn starts, at most
// maxTeamsTurnSeconds (and not past the transcript's End, when known).
func (t *TeamsTranscript) Spans(created time.Time) []TeamsSpan {
	base := t.Start.Sub(created).Seconds()
	end := math.Inf(1)
	if !t.End.IsZero() && t.End.After(t.Start) {
		end = t.End.Sub(t.Start).Seconds()
	}
	spans := make([]TeamsSpan, 0, len(t.Turns))
	for i, tr := range t.Turns {
		stop := tr.Offset + maxTeamsTurnSeconds
		// Until the next turn that starts later: turns that overlap share
		// a start time, and each lasts as long as the other.
		for _, next := range t.Turns[i+1:] {
			if next.Offset > tr.Offset {
				stop = math.Min(stop, next.Offset)
				break
			}
		}
		if tr.Offset < end {
			stop = math.Min(stop, end)
		}
		if stop <= tr.Offset {
			continue // past the transcript's end
		}
		spans = append(spans, TeamsSpan{Speaker: tr.Speaker, Start: base + tr.Offset, End: base + stop, Text: tr.Text})
	}
	return spans
}
