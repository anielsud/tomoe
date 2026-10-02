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
		"Q3 retro/Q4 dev-agent":   {"q", "3", "retro", "q", "4", "dev", "agent"},
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

func TestWords_SpokenNumbersFillersAndOK(t *testing.T) {
	cases := map[string]string{
		"we shipped thirty three features":          "we shipped 33 features",
		"Um, we hit eighty one percent":             "we hit 81 percent",
		"over a hundred and thirty capabilities":    "over 130 capabilities",
		"Q three retro, Q four":                     "q 3 retro q 4",
		"twenty twenty six was two point five x":    "2026 was 2.5 x",
		"the fifteenth and 15th":                    "the 15 and 15",
		"three point two million, two thousand ten": "3.2 million 2010",
		"a lot of work, uh, OK":                     "a lot of work okay",
		"one two three":                             "1 2 3",
		"first of all, a second":                    "first of all a second",
		"six hundred sixty six customers":           "666 customers",
		"nineteen ninety":                           "1990",
	}
	for in, want := range cases {
		if got := strings.Join(Words(in), " "); got != want {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWords_TeamsAndParakeetFormattingAgree(t *testing.T) {
	// The same sentence as each system writes it: they must normalize to
	// the same words, so formatting alone scores no errors.
	pairs := [][2]string{
		{"We shipped 33 features this past quarter. We hit 81% of our commits.",
			"Um I don't know, we shipped thirty three features this past quarter. Um we hit eighty one percent of our commits."},
		{"Q3 retro Q4 for experience optimization.", "Q three, retro, Q four, for experience optimization."},
		{"exactly 666 customers", "exactly six hundred sixty six customers"},
	}
	for _, p := range pairs {
		ref, hyp := Words(p[0]), Words(p[1])
		// The hypothesis has extra words ("i don't know") in the first
		// pair; only those may count.
		e := Align(ref, hyp)
		if e.Substitutions != 0 || e.Deletions != 0 {
			t.Errorf("%q vs %q: %+v\nref %q\nhyp %q", p[0], p[1], e, ref, hyp)
		}
	}
}

const reviewedTranscript = `Review
Host: X

Sam Rivera  10:56
If I recall the first one was about decisions.

Jordan Lee 11:08
Oh there was the feedback one, yes

Sam Rivera 11:08
when we were originally doing decision scope to session scope is the change that we made and then more words here

Jordan Lee  11:30
Is this an indication?
Stanley said we meet at 3:00
Notaname 11:40
`

func TestParse_LooseHeadersOverlapsAndWarnings(t *testing.T) {
	ref := mustParse(t, reviewedTranscript, 60*12)
	if len(ref.Turns) != 4 {
		t.Fatalf("turns = %d: %+v", len(ref.Turns), ref.Turns)
	}
	// The single-space headers for known speakers are turns, and the two
	// 11:08 turns overlap: the interjection ends after its own estimated
	// length, the main speaker runs to the next turn.
	rup, sat := ref.Turns[1], ref.Turns[2]
	if rup.Speaker != "Jordan Lee" || sat.Speaker != "Sam Rivera" || rup.Start != 668 || sat.Start != 668 {
		t.Fatalf("overlap turns = %+v, %+v", rup, sat)
	}
	if sat.End != 690 || rup.End >= sat.End || rup.End <= rup.Start {
		t.Errorf("ends: interjection %.1f, main %.1f", rup.End, sat.End)
	}
	if !ref.Overlapping(1) || !ref.Overlapping(2) || ref.Overlapping(0) {
		t.Error("Overlapping() wrong")
	}
	// "we meet at 3:00" isn't a known speaker, so it stays text and warns;
	// so does "Notaname 11:40".
	if !strings.Contains(ref.Turns[3].Text, "we meet at 3:00") || len(ref.Warnings) != 2 {
		t.Errorf("last turn %q, warnings %q", ref.Turns[3].Text, ref.Warnings)
	}
}

func TestScoreSpeakers_OverlapEitherSpeakerCounts(t *testing.T) {
	// B interjects over A for 2-4s.
	ref := &Reference{Turns: []Turn{{"A", 0, 10, "a a a a a a a a a a a a a a a a a a a"}, {"B", 2, 4, "b"}}}
	ref.Turns[0], ref.Turns[1] = Turn{"B", 2, 4, "b"}, Turn{"A", 2, 10, "a a a a"}
	ref.Turns = append([]Turn{{"A", 0, 2, "a"}}, ref.Turns...)
	// Hypothesis says A the whole time: still right during the overlap.
	s := ScoreSpeakers(ref, []Labeled{{0, 10, "P1", ""}}, 0)
	if s.Confusion != 0 {
		t.Errorf("confusion = %.3f, want 0 (either speaker counts during overlap)", s.Confusion)
	}
}

func TestScoreAnnotatedOverlaps(t *testing.T) {
	ref := mustParse(t, reviewedTranscript, 60*12)
	hyp := []Labeled{
		{656, 668, "P1", "if i recall the first one was about decisions"},
		{668, 670, "P2", "oh there was the feedback one yes"},
		{668, 690, "P1", "when we were originally doing decision scope"},
	}
	s := ScoreAnnotatedOverlaps(ref, hyp, map[string]string{"P1": "Sam Rivera", "P2": "Jordan Lee"}, 3)
	if s.Interjections != 1 || s.Found != 1 || s.RightSpeaker != 1 || s.DetectedSeconds <= 0 || s.Seconds <= 0 {
		t.Errorf("overlaps = %+v", s)
	}
}

func TestAlignPairs(t *testing.T) {
	ref := strings.Fields("the cat sat on the mat")
	got := alignPairs(ref, strings.Fields("the bat sat the mat today"))
	// the=0, bat~cat=1, sat=2, ("on" deleted), the=4, mat=5, today inserted
	want := []int{0, 1, 2, 4, 5, -1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("alignPairs = %v, want %v", got, want)
	}
}

func TestWordAlignment_ScoresShortTurnsAndOverlapByText(t *testing.T) {
	// A talks, B interjects "yes exactly" (same start: overlap), A carries
	// on. Times don't matter: words are matched to turns by text.
	ref := &Reference{Turns: []Turn{
		{"A", 0, 5, "we shipped thirty three features this quarter"},
		{"B", 5, 6, "yes exactly"},
		{"A", 5, 9, "and retention held at seventy five percent"},
	}}
	texts := strings.Fields("we shipped 33 features this quarter yes exactly and retention held at 75%")
	a := NewWordAlignment(ref, texts)
	lab := make([][]string, len(texts))
	for i := range lab {
		lab[i] = []string{"P1"} // everything given to A
	}
	s := a.Score(lab, map[string]string{"P1": "A", "P2": "B"})
	// 14 normalized words each side ("33" and "75 percent" normalize to
	// match), all matched; B's 2 are wrong.
	if s.Words != 14 || s.RefWords != 14 {
		t.Errorf("scored %d of %d reference words, want 14 of 14", s.Words, s.RefWords)
	}
	if s.Correct != s.Words-2 {
		t.Errorf("correct = %d of %d, want all but B's 2", s.Correct, s.Words)
	}
	short := s.Buckets[0]
	if short.Words != 2 || short.Correct != 0 {
		t.Errorf("1-3 word bucket = %+v, want B's 2 words, both wrong", short)
	}
	// Labeling the interjection right fixes exactly those 2.
	lab[6], lab[7] = []string{"P2"}, []string{"P2"}
	if s := a.Score(lab, map[string]string{"P1": "A", "P2": "B"}); s.Correct != s.Words {
		t.Errorf("all right: %d of %d", s.Correct, s.Words)
	}
}

func TestSpeakersAt(t *testing.T) {
	hyp := []Labeled{{0, 5, "A", ""}, {4, 6, "B", ""}}
	got := SpeakersAt(hyp, []float64{1, 4.5, 7})
	if !reflect.DeepEqual(got, [][]string{{"A"}, {"A", "B"}, nil}) {
		t.Errorf("SpeakersAt = %v", got)
	}
}
