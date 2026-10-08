package diarize

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestNameShortLines(t *testing.T) {
	hints := []Hint{
		{T: 10.6, Name: "Ana Lopez"}, {T: 11.2, Name: "Ana Lopez"}, // during the 10.0-11.0 line
		{T: 20.5, Name: "Ana Lopez"}, {T: 21.0, Name: "Ben Ito"}, // two people during 20.0-21.0
		{T: 30.7, Candidates: []string{"Ana Lopez", "Ben Ito"}}, // several lit
	}
	labelOf := func(name string) (string, bool) {
		return map[string]string{"Ana Lopez": "Person 1 (Ana Lopez)", "Ben Ito": "Person 2 (Ben Ito)"}[name], true
	}
	segs := []session.Segment{
		{Source: "monitor", Speaker: "Person 2 (Ben Ito)", StartTime: 10, EndTime: 11}, // renamed to Ana
		{Source: "monitor", Speaker: "Person 2 (Ben Ito)", StartTime: 20, EndTime: 21}, // highlight unclear: kept
		{Source: "monitor", Speaker: "Person 2 (Ben Ito)", StartTime: 30, EndTime: 31}, // several lit: kept
		{Source: "monitor", Speaker: "Person 2 (Ben Ito)", StartTime: 9, EndTime: 13},  // too long: kept
		{Source: "mic", Speaker: "You", StartTime: 10, EndTime: 11},                    // the host's own line: kept
		{Source: "monitor", Speaker: "Person 2 (Ben Ito)", StartTime: 50, EndTime: 51}, // no hints: kept
	}
	if n := NameShortLines(segs, hints, labelOf); n != 1 {
		t.Errorf("changed %d lines, want 1", n)
	}
	want := []string{"Person 1 (Ana Lopez)", "Person 2 (Ben Ito)", "Person 2 (Ben Ito)", "Person 2 (Ben Ito)", "You", "Person 2 (Ben Ito)"}
	for i, s := range segs {
		if s.Speaker != want[i] {
			t.Errorf("line %d: %q, want %q", i, s.Speaker, want[i])
		}
	}
}
