package eval

import (
	"strings"
	"unicode"
)

// Words normalizes text for word-error scoring, identically for reference
// and hypothesis so formatting differences don't count as errors:
// lowercase; punctuation dropped (keeping apostrophes inside words and
// "2.5"/"1,000" together); "%" spelled "percent"; letters and digits split
// ("q3" -> "q 3"); filler words dropped; "ok" as "okay"; and spoken
// numbers turned into digits ("thirty three" -> "33", "a hundred and
// thirty" -> "130", "twenty twenty six" -> "2026", "fifteenth" -> "15"),
// since Teams writes digits where Parakeet often spells numbers out.
func Words(text string) []string {
	text = strings.ToLower(text)
	text = strings.ReplaceAll(text, "%", " percent ")
	var b strings.Builder
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			b.WriteRune(r)
		case r == '.' || r == ',':
			// Keep "2.5" and "1,000" together, drop sentence punctuation.
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	var toks []string
	for _, w := range strings.Fields(b.String()) {
		w = strings.Trim(w, ".,'")
		if strings.ContainsAny(w, ".,") && !isNumber(w) {
			// A sentence-ending "." or "," glued between words ("end.next").
			for _, part := range strings.FieldsFunc(w, func(r rune) bool { return r == '.' || r == ',' }) {
				toks = append(toks, splitAlnum(part)...)
			}
			continue
		}
		if w != "" {
			toks = append(toks, splitAlnum(strings.ReplaceAll(w, ",", ""))...)
		}
	}
	var kept []string
	for _, w := range toks {
		if fillers[w] {
			continue
		}
		if w == "ok" {
			w = "okay"
		}
		kept = append(kept, w)
	}
	return spokenNumbers(kept)
}

// fillers are hesitation sounds Teams drops and Parakeet often keeps.
var fillers = map[string]bool{"um": true, "uh": true, "uhm": true, "umm": true, "erm": true, "er": true, "hmm": true, "mm": true, "mhm": true, "ah": true}

// splitAlnum splits a token at letter/digit boundaries ("q3" -> "q", "3";
// "2x" -> "2", "x"), dropping an ordinal suffix after digits ("15th" ->
// "15"). Contractions and pure words or numbers pass through unchanged.
func splitAlnum(w string) []string {
	if strings.ContainsRune(w, '\'') {
		return []string{w}
	}
	var parts []string
	start := 0
	runes := []rune(w)
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || isDigitish(runes[i]) != isDigitish(runes[i-1]) {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	var out []string
	for i, p := range parts {
		if i > 0 && isNumber(parts[i-1]) && (p == "st" || p == "nd" || p == "rd" || p == "th") {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isDigitish(r rune) bool { return unicode.IsDigit(r) || r == '.' }

func isNumber(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) && r != '.' && r != ',' {
			return false
		}
	}
	return true
}

// ErrorCounts is a word alignment's edit breakdown against a reference.
type ErrorCounts struct {
	RefWords      int `json:"ref_words"`
	Substitutions int `json:"substitutions"`
	Deletions     int `json:"deletions"`  // reference words missing from the hypothesis
	Insertions    int `json:"insertions"` // hypothesis words with no reference word
}

// Errors is the total number of edits.
func (e ErrorCounts) Errors() int { return e.Substitutions + e.Deletions + e.Insertions }

// Rate is the word error rate: edits per reference word.
func (e ErrorCounts) Rate() float64 {
	if e.RefWords == 0 {
		if e.Insertions > 0 {
			return 1
		}
		return 0
	}
	return float64(e.Errors()) / float64(e.RefWords)
}

// Add sums two sets of counts.
func (e ErrorCounts) Add(o ErrorCounts) ErrorCounts {
	return ErrorCounts{e.RefWords + o.RefWords, e.Substitutions + o.Substitutions, e.Deletions + o.Deletions, e.Insertions + o.Insertions}
}

// Align computes the minimum-edit word alignment of hyp against ref and its
// edit breakdown. Two rolling rows keep memory linear, so an hour-long
// meeting (~10k words each side) is fine.
func Align(ref, hyp []string) ErrorCounts {
	type cell struct{ cost, sub, del, ins int32 }
	prev := make([]cell, len(hyp)+1)
	cur := make([]cell, len(hyp)+1)
	for j := range prev {
		prev[j] = cell{cost: int32(j), ins: int32(j)}
	}
	for i := 1; i <= len(ref); i++ {
		cur[0] = cell{cost: int32(i), del: int32(i)}
		for j := 1; j <= len(hyp); j++ {
			diag := prev[j-1]
			if ref[i-1] != hyp[j-1] {
				diag.cost++
				diag.sub++
			}
			del := prev[j]
			del.cost++
			del.del++
			ins := cur[j-1]
			ins.cost++
			ins.ins++
			best := diag
			if del.cost < best.cost {
				best = del
			}
			if ins.cost < best.cost {
				best = ins
			}
			cur[j] = best
		}
		prev, cur = cur, prev
	}
	last := prev[len(hyp)]
	return ErrorCounts{RefWords: len(ref), Substitutions: int(last.sub), Deletions: int(last.del), Insertions: int(last.ins)}
}
