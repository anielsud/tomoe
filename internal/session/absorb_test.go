package session

import (
	"strings"
	"testing"
)

func line(speaker, source string, start, end float64, nWords int) Segment {
	return Segment{Speaker: speaker, Source: source, StartTime: start, EndTime: end, Text: strings.TrimSpace(strings.Repeat("word ", nWords))}
}

func speakers(segs []Segment) []string {
	var out []string
	for _, s := range segs {
		out = append(out, s.Speaker)
	}
	return out
}

func TestAbsorbSmallSpeakers(t *testing.T) {
	segs := []Segment{
		line("Person 1 (Ana)", "monitor", 0, 10, 30),
		line("Person 7", "monitor", 10, 10.8, 2), // split off Ana's sentence: continues her
		line("Person 1 (Ana)", "monitor", 10.8, 20, 25),
		line("You", "mic", 21, 30, 25),
		line("Person 9", "monitor", 25, 25.5, 1), // backchannel 5 s after Ana: joins the next line
		line("Person 2 (Ben)", "monitor", 31, 40, 25),
		line("Person 3 (Cy)", "monitor", 41, 44, 4), // renamed by the user: kept
	}
	keep := func(label string) bool { return label == "Person 3 (Cy)" }
	if n := AbsorbSmallSpeakers(segs, 20, keep); n != 2 {
		t.Fatalf("changed %d lines, want 2", n)
	}
	want := []string{"Person 1 (Ana)", "Person 1 (Ana)", "Person 1 (Ana)", "You", "Person 2 (Ben)", "Person 2 (Ben)", "Person 3 (Cy)"}
	for i, got := range speakers(segs) {
		if got != want[i] {
			t.Errorf("line %d: %q, want %q", i, got, want[i])
		}
	}
}

func TestAbsorbSmallSpeakers_Off(t *testing.T) {
	segs := []Segment{line("Person 1", "monitor", 0, 10, 30), line("Person 2", "monitor", 10, 11, 1)}
	if n := AbsorbSmallSpeakers(segs, 0, nil); n != 0 || segs[1].Speaker != "Person 2" {
		t.Errorf("minWords 0 changed %d lines (%v)", n, speakers(segs))
	}
}

func TestAbsorbSmallSpeakers_EverySpeakerSmall(t *testing.T) {
	// Nothing big to give the lines to: leave them.
	segs := []Segment{line("Person 1", "monitor", 0, 1, 2), line("Person 2", "monitor", 1, 2, 2)}
	if n := AbsorbSmallSpeakers(segs, 20, nil); n != 0 {
		t.Errorf("changed %d lines with no large speaker", n)
	}
}
