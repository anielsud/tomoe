package eval

import (
	"sort"
	"strings"
)

// A TextHunk is one place where a transcript's words disagree with the
// reference's: Ref[RefFrom:RefTo] against Hyp[HypFrom:HypTo] (normalized
// words, see Words), with a Kind for disagreements that don't change
// meaning.
type TextHunk struct {
	RefFrom, RefTo int
	HypFrom, HypTo int
	Kind           string
}

// Kinds of TextHunk that don't change what was said.
const (
	KindMeaningful = ""            // anything not below
	KindSmallWords = "small words" // only function words differ (the/a, and/but)
	KindWordForm   = "word form"   // same words, plural or tense differs
	KindSplitJoin  = "split/join"  // the same letters split or joined differently
	KindLongRun    = "long run"    // 8+ words only one side has: a stretch one side didn't hear (or didn't record)
)

// DiffText aligns hyp to ref (minimum edits) and returns every hunk where
// they differ, in order.
func DiffText(ref, hyp []string) []TextHunk {
	pairs := alignPairs(ref, hyp)
	var hunks []TextHunk
	i, j := 0, 0
	open := -1 // index into hunks of the hunk being grown, or -1
	grow := func(ri, hj int) {
		if open < 0 {
			hunks = append(hunks, TextHunk{RefFrom: ri, RefTo: ri, HypFrom: hj, HypTo: hj})
			open = len(hunks) - 1
		}
	}
	for i < len(ref) || j < len(hyp) {
		switch {
		case j < len(hyp) && pairs[j] == i && i < len(ref) && ref[i] == hyp[j]:
			open = -1
			i++
			j++
		case j < len(hyp) && pairs[j] == i && i < len(ref):
			grow(i, j)
			i++
			j++
			hunks[open].RefTo, hunks[open].HypTo = i, j
		case j < len(hyp) && pairs[j] == -1:
			grow(i, j)
			j++
			hunks[open].HypTo = j
		default:
			grow(i, j)
			i++
			hunks[open].RefTo = i
		}
	}
	for k := range hunks {
		h := &hunks[k]
		h.Kind = hunkKind(ref[h.RefFrom:h.RefTo], hyp[h.HypFrom:h.HypTo])
	}
	return hunks
}

func hunkKind(r, h []string) string {
	switch {
	case (len(r) >= 8 && len(h) == 0) || (len(h) >= 8 && len(r) == 0):
		return KindLongRun
	case strings.Join(r, "") == strings.Join(h, ""):
		return KindSplitJoin
	case allFunctionWords(r) && allFunctionWords(h):
		return KindSmallWords
	case len(r) == len(h) && sameStems(r, h):
		return KindWordForm
	}
	return KindMeaningful
}

func allFunctionWords(ws []string) bool {
	for _, w := range ws {
		if !functionWords[w] {
			return false
		}
	}
	return true
}

func sameStems(a, b []string) bool {
	for i := range a {
		if stem(a[i]) != stem(b[i]) {
			return false
		}
	}
	return true
}

func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s", "'s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// functionWords are words whose swap or loss rarely changes meaning, and
// which Teams' transcript tidies away more than Parakeet does.
var functionWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and but so or to of in on at for with from by as it its it's this that these
		those i i'm you we they he she is are was were be been am do does did have has had not no yes yeah just like i've
		we're you're they're there their our your my his her them us me if then than what which who how when where really
		very kind sort mean know think well right okay oh will would can could should gonna going get got all also about up
		out`) {
		functionWords[w] = true
	}
}

// A TextSpot is a place in a reference transcript where an earlier
// transcript got something meaningful wrong, judged by hand or by review
// (see tomoe textdiff): Words, between Before and After, as the reference
// says them. Located by that context, so it survives any change to the
// transcript being scored.
type TextSpot struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Term     string `json:"term,omitempty"`
	Before   string `json:"before"`
	Words    string `json:"words"`
	After    string `json:"after"`
	// Pos is roughly where in the reference it is (0-1), to pick between
	// repeats of the same context.
	Pos float64 `json:"pos"`
}

// SpotResult says whether a transcript now has a spot's words right.
type SpotResult struct {
	Spot  TextSpot
	Found bool // located in the reference
	Fixed bool // every reference word of the spot matched
}

// ScoreSpots locates each spot in ref and reports whether hyp (aligned as
// by DiffText) has its words exactly.
func ScoreSpots(ref, hyp []string, spots []TextSpot) []SpotResult {
	pairs := alignPairs(ref, hyp)
	matched := make([]bool, len(ref))
	for j, i := range pairs {
		if i >= 0 && ref[i] == hyp[j] {
			matched[i] = true
		}
	}
	out := make([]SpotResult, len(spots))
	for k, sp := range spots {
		out[k].Spot = sp
		from, to, ok := locate(ref, Words(sp.Before), Words(sp.Words), Words(sp.After), sp.Pos)
		if !ok {
			continue
		}
		out[k].Found = true
		out[k].Fixed = true
		for i := from; i < to; i++ {
			if !matched[i] {
				out[k].Fixed = false
			}
		}
	}
	return out
}

// locate finds before+words+after in ref, nearest pos when it repeats,
// and returns where words is.
func locate(ref, before, words, after []string, pos float64) (from, to int, ok bool) {
	if len(words) == 0 {
		return 0, 0, false
	}
	seq := append(append(append([]string(nil), before...), words...), after...)
	best, bestD := -1, 2.0
	for i := 0; i+len(seq) <= len(ref); i++ {
		if equalWords(ref[i:i+len(seq)], seq) {
			d := pos - float64(i)/float64(len(ref))
			if d < 0 {
				d = -d
			}
			if d < bestD {
				best, bestD = i, d
			}
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	from = best + len(before)
	return from, from + len(words), true
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TermHits counts, for each term, how often the reference says it and how
// often hyp has it at the same place (aligned as by DiffText). Terms are
// matched as normalized word sequences.
func TermHits(ref, hyp []string, terms []string) map[string][2]int {
	pairs := alignPairs(ref, hyp)
	matched := make([]bool, len(ref))
	for j, i := range pairs {
		if i >= 0 && ref[i] == hyp[j] {
			matched[i] = true
		}
	}
	out := map[string][2]int{}
	for _, t := range terms {
		tw := Words(t)
		if len(tw) == 0 {
			continue
		}
		var said, right int
		for i := 0; i+len(tw) <= len(ref); i++ {
			if !equalWords(ref[i:i+len(tw)], tw) {
				continue
			}
			said++
			all := true
			for k := i; k < i+len(tw); k++ {
				all = all && matched[k]
			}
			if all {
				right++
			}
		}
		if said > 0 {
			out[t] = [2]int{said, right}
		}
	}
	return out
}

// SortedTerms returns hits' terms, most often said first.
func SortedTerms(hits map[string][2]int) []string {
	terms := make([]string, 0, len(hits))
	for t := range hits {
		terms = append(terms, t)
	}
	sort.Slice(terms, func(i, j int) bool {
		if hits[terms[i]][0] != hits[terms[j]][0] {
			return hits[terms[i]][0] > hits[terms[j]][0]
		}
		return terms[i] < terms[j]
	})
	return terms
}
