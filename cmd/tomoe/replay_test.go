package main

import (
	"math"
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestWordDistance(t *testing.T) {
	cases := []struct {
		a, b []string
		want int
	}{
		{nil, nil, 0},
		{[]string{"a", "b"}, nil, 2},
		{nil, []string{"a"}, 1},
		{[]string{"the", "cat", "sat"}, []string{"the", "cat", "sat"}, 0},
		{[]string{"the", "cat", "sat"}, []string{"the", "bat", "sat"}, 1},
		{[]string{"the", "cat", "sat"}, []string{"the", "sat"}, 1},
		{[]string{"the", "cat"}, []string{"a", "the", "cat", "too"}, 2},
	}
	for _, c := range cases {
		if got := wordDistance(c.a, c.b); got != c.want {
			t.Errorf("wordDistance(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestTranscriptWords_NormalizesAndFiltersBySource(t *testing.T) {
	segs := []session.Segment{
		{Source: "monitor", Text: "Hello, World!"},
		{Source: "mic", Text: "It's 4%."},
	}
	if got := transcriptWords(segs, "monitor"); len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Errorf("monitor words = %v", got)
	}
	if got := transcriptWords(segs, ""); len(got) != 4 || got[2] != "it's" || got[3] != "4" {
		t.Errorf("all words = %v", got)
	}
}

func mon(speaker string, start, end float64) session.Segment {
	return session.Segment{Source: "monitor", Speaker: speaker, StartTime: start, EndTime: end}
}

func TestSpeakerAgreement_IgnoresLabelNames(t *testing.T) {
	a := []session.Segment{mon("Person 1", 0, 10), mon("Person 2", 10, 20)}
	b := []session.Segment{mon("Person 2", 0, 10), mon("Person 1", 10, 20)}
	agreed, total := speakerAgreement(a, b)
	if total != 20 || agreed != 20 {
		t.Errorf("agreement = %v of %v, want 20 of 20", agreed, total)
	}
}

func TestSpeakerAgreement_CountsASplitSpeakerOnce(t *testing.T) {
	// b splits a's Person 1 into two clusters: only the larger half can
	// map to it one-to-one.
	a := []session.Segment{mon("Person 1", 0, 10), mon("Person 1", 10, 14), mon("Person 2", 14, 20)}
	b := []session.Segment{mon("Person 1", 0, 10), mon("Person 3", 10, 14), mon("Person 2", 14, 20)}
	agreed, total := speakerAgreement(a, b)
	if total != 20 || math.Abs(agreed-16) > 1e-9 {
		t.Errorf("agreement = %v of %v, want 16 of 20", agreed, total)
	}
}

func TestSpeakerAgreement_IgnoresMic(t *testing.T) {
	a := []session.Segment{{Source: "mic", Speaker: "You", StartTime: 0, EndTime: 5}}
	if agreed, total := speakerAgreement(a, a); agreed != 0 || total != 0 {
		t.Errorf("agreement = %v of %v, want 0 of 0", agreed, total)
	}
}
