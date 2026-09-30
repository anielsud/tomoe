package eval

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

const sampleTranscript = `Weekly sync
Host: Someone
Date: 4:01 PM on Sep 29, 2026
Length: 1m10s

Alice Adams  0:00
Hello everyone.

Bob Brown  0:05
Hi, shall we start?

Alice Adams  0:09
Yes. Revenue came in 4% above forecast,
mostly from renewals.

Bob Brown  1:02
Great.
`

func mustParse(t *testing.T, s string, end float64) *Reference {
	t.Helper()
	ref, err := ParseTeamsTranscript(strings.NewReader(s), end)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestParseTeamsTranscript(t *testing.T) {
	ref := mustParse(t, sampleTranscript, 70)
	if ref.Title != "Weekly sync" {
		t.Errorf("title = %q", ref.Title)
	}
	want := []Turn{
		{"Alice Adams", 0, 5, "Hello everyone."},
		{"Bob Brown", 5, 9, "Hi, shall we start?"},
		{"Alice Adams", 9, 62, "Yes. Revenue came in 4% above forecast, mostly from renewals."},
		{"Bob Brown", 62, 70, "Great."},
	}
	if !reflect.DeepEqual(ref.Turns, want) {
		t.Errorf("turns =\n%+v\nwant\n%+v", ref.Turns, want)
	}
	if got := ref.Speakers(); !reflect.DeepEqual(got, []string{"Alice Adams", "Bob Brown"}) {
		t.Errorf("speakers = %v", got)
	}
}

func TestParseTeamsTranscript_HourTimestamps(t *testing.T) {
	ref := mustParse(t, "T\n\nA  59:59\nx\n\nB  1:00:05\ny\n", 3700)
	if ref.Turns[1].Start != 3605 || ref.Turns[0].End != 3605 {
		t.Errorf("turns = %+v", ref.Turns)
	}
}

func TestWords(t *testing.T) {
	cases := map[string][]string{
		"Hello, World!":           {"hello", "world"},
		"It's 81% of 2.5 million": {"it's", "81", "percent", "of", "2.5", "million"},
		"Q3 retro/Q4 dev-agent":   {"q3", "retro", "q4", "dev", "agent"},
		"1,000 users. Then more":  {"1000", "users", "then", "more"},
		"end.next":                {"end", "next"},
	}
	for in, want := range cases {
		if got := Words(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlign(t *testing.T) {
	ref := strings.Fields("the cat sat on the mat")
	cases := []struct {
		hyp  string
		want ErrorCounts
	}{
		{"the cat sat on the mat", ErrorCounts{6, 0, 0, 0}},
		{"the bat sat on the mat", ErrorCounts{6, 1, 0, 0}},
		{"the cat on the mat", ErrorCounts{6, 0, 1, 0}},
		{"the cat sat on the mat today", ErrorCounts{6, 0, 0, 1}},
		{"", ErrorCounts{6, 0, 6, 0}},
	}
	for _, c := range cases {
		got := Align(ref, strings.Fields(c.hyp))
		if got != c.want {
			t.Errorf("Align(%q) = %+v, want %+v", c.hyp, got, c.want)
		}
	}
	if r := (ErrorCounts{RefWords: 4, Substitutions: 1, Insertions: 1}).Rate(); r != 0.5 {
		t.Errorf("Rate = %v", r)
	}
}

// bruteBest is the best total weight over all assignments, for checking
// maxWeightMatching on small random matrices.
func bruteBest(w [][]float64, row int, used map[int]bool) float64 {
	if row == len(w) {
		return 0
	}
	best := bruteBest(w, row+1, used) // row unmatched
	for c := range w[row] {
		if used[c] {
			continue
		}
		used[c] = true
		best = math.Max(best, w[row][c]+bruteBest(w, row+1, used))
		delete(used, c)
	}
	return best
}

func TestMaxWeightMatching_Optimal(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		rows, cols := 1+rng.Intn(5), 1+rng.Intn(5)
		w := make([][]float64, rows)
		for i := range w {
			w[i] = make([]float64, cols)
			for j := range w[i] {
				w[i][j] = float64(rng.Intn(10))
			}
		}
		assign := maxWeightMatching(w)
		got, seen := 0.0, map[int]bool{}
		for i, c := range assign {
			if c < 0 {
				continue
			}
			if seen[c] {
				t.Fatalf("column %d assigned twice in %v", c, assign)
			}
			seen[c] = true
			got += w[i][c]
		}
		if want := bruteBest(w, 0, map[int]bool{}); got != want {
			t.Fatalf("matching %v on %v: weight %v, want %v", assign, w, got, want)
		}
	}
}

// twoSpeakers is a reference with Alice for 0-10s and Bob for 10-20s.
func twoSpeakers() *Reference {
	return &Reference{Turns: []Turn{
		{"Alice", 0, 10, "a a a"},
		{"Bob", 10, 20, "b b b"},
	}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestScoreSpeakers(t *testing.T) {
	ref := twoSpeakers()
	cases := []struct {
		name                     string
		hyp                      []Labeled
		confusion, purity, cover float64
		hypSpeakers              int
	}{
		{"perfect, labels renamed", []Labeled{{0, 10, "P2", ""}, {10, 20, "P1", ""}}, 0, 1, 1, 2},
		{"one speaker for both", []Labeled{{0, 20, "P1", ""}}, 0.5, 0.5, 1, 1},
		{"Bob split in two", []Labeled{{0, 10, "P1", ""}, {10, 15, "P2", ""}, {15, 20, "P3", ""}}, 0.25, 1, 0.75, 3},
	}
	for _, c := range cases {
		// Collar 0 so the arithmetic is exact.
		s := ScoreSpeakers(ref, c.hyp, 0)
		if !near(s.Confusion, c.confusion) || !near(s.Purity, c.purity) || !near(s.Coverage, c.cover) || s.HypSpeakers != c.hypSpeakers {
			t.Errorf("%s: got confusion %.3f purity %.3f coverage %.3f speakers %d", c.name, s.Confusion, s.Purity, s.Coverage, s.HypSpeakers)
		}
	}
}

func TestScoreSpeakers_CollarAndSilence(t *testing.T) {
	ref := twoSpeakers()
	// Speech only 2-8s and 12-18s: 12s scored, not 20.
	s := ScoreSpeakers(ref, []Labeled{{2, 8, "A", ""}, {12, 18, "B", ""}}, 0)
	if !near(s.ScoredSeconds, 12) || s.Confusion != 0 {
		t.Errorf("scored %.2f confusion %.3f", s.ScoredSeconds, s.Confusion)
	}
	// A 1s collar drops 9-11s, so a wrong label there doesn't count.
	s = ScoreSpeakers(ref, []Labeled{{0, 10.9, "A", ""}, {10.9, 20, "B", ""}}, 1)
	if s.Confusion != 0 {
		t.Errorf("confusion inside collar = %.3f, want 0", s.Confusion)
	}
}

func TestScoreSpeakers_Overlap(t *testing.T) {
	s := ScoreSpeakers(twoSpeakers(), []Labeled{{0, 11, "A", ""}, {9, 20, "B", ""}}, 0)
	if !near(s.OverlapSeconds, 2) || s.Confusion != 0 {
		t.Errorf("overlap %.2f confusion %.3f", s.OverlapSeconds, s.Confusion)
	}
}

func TestScoreDetection(t *testing.T) {
	ref := &Reference{Turns: []Turn{{"A", 0, 10, "one two"}, {"B", 10, 20, "three four five"}}}
	s := ScoreDetection(ref, []Labeled{{0.5, 1.5, "x", ""}}, 0.5)
	if s.Turns != 2 || s.TurnsHeard != 1 || s.Words != 5 || s.WordsHeard != 2 {
		t.Errorf("detection = %+v", s)
	}
}

func TestSpeakerAttributedErrors(t *testing.T) {
	ref := &Reference{Turns: []Turn{{"A", 0, 5, "hello there"}, {"B", 5, 10, "good morning"}}}
	hyp := []Labeled{
		{0, 5, "P1", "hello there"},
		{5, 10, "P1", "good morning"}, // right words, wrong speaker
	}
	got := SpeakerAttributedErrors(ref, hyp, map[string]string{"P1": "A"})
	// A gets "hello there good morning" (2 insertions), B gets nothing (2 deletions).
	if got.Errors() != 4 || got.RefWords != 4 {
		t.Errorf("errors = %+v", got)
	}
	if plain := Align(Words("hello there good morning"), Words("hello there good morning")); plain.Errors() != 0 {
		t.Errorf("plain WER sanity: %+v", plain)
	}
}

func TestScoreQuickExchanges(t *testing.T) {
	ref := &Reference{Turns: []Turn{
		{"A", 0, 10, "long point about revenue and churn"},
		{"B", 10, 11, "Yeah, exactly."},
		{"A", 11, 20, "and so we continue"},
		{"B", 20, 21, "Right."},
		{"A", 21, 30, "more"},
	}}
	hyp := []Labeled{
		{0, 9.5, "P1", "long point about revenue and churn"},
		{10, 11, "P2", "yeah exactly"},
		{11, 20, "P1", "and so we continue"}, // "Right." missed entirely
		{21, 30, "P1", "more"},
	}
	s := ScoreQuickExchanges(ref, hyp, map[string]string{"P1": "A", "P2": "B"}, 8, 6, 3)
	if s.Turns != 2 || s.Found != 1 || s.RightSpeaker != 1 {
		t.Errorf("exchanges = %+v", s)
	}
}

func TestReferenceSlice(t *testing.T) {
	ref := mustParse(t, sampleTranscript, 70)
	s := ref.Slice(5, 30)
	want := []Turn{
		{"Bob Brown", 0, 4, "Hi, shall we start?"},
		{"Alice Adams", 4, 25, "Yes. Revenue came in 4% above forecast, mostly from renewals."},
	}
	if !reflect.DeepEqual(s.Turns, want) {
		t.Errorf("Slice(5, 30) =\n%+v\nwant\n%+v", s.Turns, want)
	}
}
