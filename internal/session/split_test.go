package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestWordsFromTokens(t *testing.T) {
	// Real Parakeet output: a leading space starts a word, punctuation and
	// word pieces continue it, times are relative to the decoded audio.
	tokens := []string{" Re", "ven", "ue", " c", "ame", " in", " ", "4", "%", ",", " most", "ly"}
	ts := []float32{0.64, 0.88, 1.12, 1.28, 1.36, 1.52, 2.00, 2.16, 2.32, 3.76, 4.00, 4.24}
	got := WordsFromTokens(tokens, ts, 10, 15)
	want := []Word{
		{"Revenue", 10.64, 11.28}, {"came", 11.28, 11.52}, {"in", 11.52, 12.16},
		{"4%,", 12.16, 14.00}, {"mostly", 14.00, 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WordsFromTokens =\n%v\nwant\n%v", got, want)
	}
	// An utterance whose first token is punctuation (this crashed live
	// transcription before the guard).
	if got := WordsFromTokens([]string{".", " yes"}, []float32{0, 0.5}, 0, 1); len(got) != 2 || got[1].Text != "yes" {
		t.Errorf("leading punctuation: %v", got)
	}
	if WordsFromTokens([]string{"a"}, nil, 0, 1) != nil {
		t.Error("mismatched inputs should give nil")
	}
}

// words builds evenly spaced words from text, 0.5s each, starting at start.
func words(text string, start float64) []Word {
	var ws []Word
	for i, w := range strings.Fields(text) {
		s := start + float64(i)*0.5
		ws = append(ws, Word{w, s, s + 0.5})
	}
	return ws
}

func labelsOf(segs []Segment) []string {
	var out []string
	for _, s := range segs {
		out = append(out, s.Speaker+": "+s.Text)
	}
	return out
}

var twoSpeakers = map[int]string{0: "Person 1", 1: "Person 2"}

func TestSplitByDiarization_HandOffInsideOneLine(t *testing.T) {
	// "...mostly from renewals. Right. New business was flat" as one line;
	// diarization hears speaker 1 say "Right." (4.5-5.5s).
	seg := Segment{ID: "seg-7", Source: "monitor", Speaker: "Person 1", StartTime: 0, EndTime: 9,
		Text:  "mostly from renewals. Right. New business was flat.",
		Words: words("mostly from renewals. Right. New business was flat.", 3)}
	diar := []DiarizeSegment{{0, 4.5, 0}, {4.5, 5.5, 1}, {5.5, 9, 0}}
	out, n := SplitByDiarization([]Segment{seg}, diar, twoSpeakers)
	want := []string{"Person 1: mostly from renewals.", "Person 2: Right. New", "Person 1: business was flat."}
	if got := labelsOf(out); !reflect.DeepEqual(got, want) || n != 3 {
		t.Errorf("split = %q (count %d), want %q", got, n, want)
	}
	if out[0].ID != "seg-7" || out[1].ID != "seg-7.2" || out[2].ID != "seg-7.3" {
		t.Errorf("IDs = %s %s %s", out[0].ID, out[1].ID, out[2].ID)
	}
	if out[1].StartTime != 4.5 || out[1].EndTime != 5.5 {
		t.Errorf("middle part times %.1f-%.1f", out[1].StartTime, out[1].EndTime)
	}
}

func TestSplitByDiarization_AbsorbsBlipsShorterThanMinRun(t *testing.T) {
	seg := Segment{ID: "s", Source: "monitor", StartTime: 0, EndTime: 3,
		Text: "a b c", Words: []Word{{"a", 0, 1}, {"b", 1, 1.1}, {"c", 1.1, 3}}}
	// Speaker 1 only covers "b" for 0.1s: a boundary wobble, not a turn.
	diar := []DiarizeSegment{{0, 1, 0}, {1, 1.1, 1}, {1.1, 3, 0}}
	out, _ := SplitByDiarization([]Segment{seg}, diar, twoSpeakers)
	if got := labelsOf(out); !reflect.DeepEqual(got, []string{"Person 1: a b c"}) {
		t.Errorf("split = %q, want one line", got)
	}
}

func TestSplitByDiarization_OverlapPrefersTheShorterTurn(t *testing.T) {
	// Speaker 1 cuts in over speaker 0 (0-10s) for 4-6s.
	seg := Segment{ID: "s", Source: "monitor", StartTime: 0, EndTime: 10, Text: "x",
		Words: []Word{{"one", 0, 4}, {"yes", 4, 6}, {"two", 6, 10}}}
	diar := []DiarizeSegment{{0, 10, 0}, {4, 6, 1}}
	out, _ := SplitByDiarization([]Segment{seg}, diar, twoSpeakers)
	want := []string{"Person 1: one", "Person 2: yes", "Person 1: two"}
	if got := labelsOf(out); !reflect.DeepEqual(got, want) {
		t.Errorf("split = %q, want %q", got, want)
	}
}

func TestSplitByDiarization_NoWordsOrNotDiarizableKeepsOneLine(t *testing.T) {
	segs := []Segment{
		{ID: "a", Source: "monitor", StartTime: 0, EndTime: 4, Text: "no timings"},
		{ID: "b", Source: "mic", Speaker: "You", StartTime: 4, EndTime: 8, Text: "mine", Words: words("mine", 4)},
	}
	diar := []DiarizeSegment{{0, 2, 0}, {2, 8, 1}}
	out, n := SplitByDiarization(segs, diar, twoSpeakers)
	want := []string{"Person 1: no timings", "You: mine"}
	if got := labelsOf(out); !reflect.DeepEqual(got, want) || n != 1 {
		t.Errorf("split = %q (count %d), want %q", got, n, want)
	}
}
