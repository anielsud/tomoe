package diarize

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func words(ts ...float64) []session.Word {
	var out []session.Word
	for i, t := range ts {
		out = append(out, session.Word{Text: string(rune('a' + i)), Start: t, End: t + 0.2})
	}
	return out
}

func TestMoveWords(t *testing.T) {
	line := func(ws []session.Word) session.Segment {
		return session.Segment{Words: ws, StartTime: ws[0].Start, EndTime: ws[len(ws)-1].End}
	}
	// The change is earlier than the cut: a's last two words go to b.
	a, b := line(words(10, 10.3, 10.6, 10.9)), line(words(11.2, 11.5))
	if n := moveWords(&a, &b, 10.5, 2); n != 2 || len(a.Words) != 2 || len(b.Words) != 4 || b.StartTime != 10.6 || a.EndTime != 10.5 {
		t.Errorf("earlier: moved %d; a %v, b %v (b starts %.1f, a ends %.1f)", n, a.Text, b.Text, b.StartTime, a.EndTime)
	}
	// Later than the cut: b's first word goes to a.
	a, b = line(words(10, 10.3)), line(words(10.6, 10.9, 11.2))
	if n := moveWords(&a, &b, 10.8, 2); n != 1 || len(a.Words) != 3 || b.StartTime != 10.9 {
		t.Errorf("later: moved %d; a %d words, b starts %.1f", n, len(a.Words), b.StartTime)
	}
	// A fragment goes whole to its neighbour; never more than maxMove.
	a, b = line(words(10, 10.3)), line(words(10.6, 10.9))
	if n := moveWords(&a, &b, 5, 2); n != 2 || len(a.Words) != 0 || a.Text != "" || len(b.Words) != 4 {
		t.Errorf("fragment: a kept %d words (moved %d)", len(a.Words), n)
	}
	a, b = line(words(10, 10.5, 11, 11.5, 12, 12.5)), line(words(13))
	if n := moveWords(&a, &b, 10.4, 1); n != 0 {
		t.Errorf("moved %d words, more than a second of speech", n)
	}
}
