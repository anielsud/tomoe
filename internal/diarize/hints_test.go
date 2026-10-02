package diarize

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestCanonicalNamesFoldsTruncations(t *testing.T) {
	c := canonicalNames([]string{"Nazanin Rame…", "Nazanin Ramezani", "Ana", "Bo"})
	if got := c["Nazanin Rame…"]; got != "Nazanin Ramezani" {
		t.Errorf("truncated name -> %q, want the full name", got)
	}
	if c["Ana"] != "Ana" || c["Bo"] != "Bo" {
		t.Errorf("short names changed: %v", c)
	}
	// A one-letter misread folds into the spelling read most.
	c = canonicalNames([]string{"Shafqat Islam", "Shafqat Islam", "Shafgat Islam"})
	if got := c["Shafgat Islam"]; got != "Shafqat Islam" {
		t.Errorf("misread -> %q, want Shafqat Islam", got)
	}
	// A longer spelling with junk after the name doesn't beat the name
	// read most (it wasn't truncated).
	c = canonicalNames([]string{"Kevin Li", "Kevin Li", "Kevin Li", "Kevin Li x"})
	if got := c["Kevin Li x"]; got != "Kevin Li" {
		t.Errorf("junk-extended read -> %q, want Kevin Li", got)
	}
	// Different people stay apart.
	c = canonicalNames([]string{"Alex Atzberger", "Alex Ambrose"})
	if c["Alex Atzberger"] == c["Alex Ambrose"] {
		t.Errorf("two people folded together: %v", c)
	}
}

func TestNameSpeakersVotes(t *testing.T) {
	turns := []session.DiarizeSegment{
		{Start: 0, End: 20, Speaker: 0},
		{Start: 20, End: 40, Speaker: 1},
		{Start: 30, End: 32, Speaker: 0}, // overlap with speaker 1
		{Start: 40, End: 60, Speaker: 2},
	}
	hints := []Hint{
		// Speaker 0: two independent reads (different 5 s buckets).
		{T: 2, Name: "Ana"}, {T: 12, Name: "Ana"},
		// Speaker 1: three reads in one bucket count once, and one during
		// overlap doesn't vote: not enough.
		{T: 21, Name: "Ben"}, {T: 22, Name: "Ben"}, {T: 23, Name: "Ben"}, {T: 31.5, Name: "Ben"},
		// Speaker 2: split between two names: no majority.
		{T: 41, Name: "Cy"}, {T: 47, Name: "Cy"}, {T: 52, Name: "Di"}, {T: 57, Name: "Di"},
	}
	got := nameSpeakers(turns, hints, 100)
	if got[0] != "Ana" {
		t.Errorf("speaker 0 = %q, want Ana", got[0])
	}
	if _, ok := got[1]; ok {
		t.Errorf("speaker 1 named %q from one burst of reads", got[1])
	}
	if _, ok := got[2]; ok {
		t.Errorf("speaker 2 named %q without a majority", got[2])
	}
	if got := nameSpeakers(turns, hints, 10); got[0] != "" {
		t.Errorf("hints past through voted: %v", got)
	}
}

func TestNewVoiceAtEnd(t *testing.T) {
	frames := func(spk0Until, spk1From int) [][]int8 {
		out := make([][]int8, 100) // 10 frames a second
		for f := range out {
			out[f] = []int8{0, 0}
			if f < spk0Until {
				out[f][0] = 1
			}
			if f >= spk1From {
				out[f][1] = 1
			}
		}
		return out
	}
	if !newVoiceAtEnd(frames(95, 95), 10) {
		t.Error("speaker 1 starting in the last second not detected")
	}
	if newVoiceAtEnd(frames(100, 200), 10) {
		t.Error("one speaker talking throughout detected as a change")
	}
	if newVoiceAtEnd(frames(0, 60), 10) {
		t.Error("speaker 1 talking for the last 4 seconds detected as new")
	}
}

func TestNameSpeakersByElimination(t *testing.T) {
	turns := []session.DiarizeSegment{
		{Start: 0, End: 20, Speaker: 0},
		{Start: 20, End: 40, Speaker: 1},
	}
	hints := []Hint{
		{T: 2, Name: "Ana"}, {T: 12, Name: "Ana"}, // speaker 0 is Ana
		// Speaker 1 talks while Ana's and Ben's tiles are both lit: not
		// Ana (she's speaker 0), so Ben.
		{T: 22, Candidates: []string{"Ana", "Ben"}},
		{T: 32, Candidates: []string{"Ben", "Ana"}},
	}
	got := nameSpeakers(turns, hints, 100)
	if got[0] != "Ana" || got[1] != "Ben" {
		t.Errorf("names = %v, want Ana and Ben", got)
	}
	// An unread lit tile could be anyone: no conclusion.
	hints[2].Candidates = []string{"Ana", ""}
	hints[3].Candidates = []string{"", "Ana"}
	if got := nameSpeakers(turns, hints, 100); got[1] != "" {
		t.Errorf("speaker 1 named %q despite unread tiles", got[1])
	}
}
