package session

import (
	"fmt"
	"math"
	"strings"
)

// WordsFromTokens groups recognizer tokens into words with session-time
// timings. tokens and timestamps are parallel (timestamps in seconds from
// the start of the decoded audio, which began at segStart); a token starting
// with a space (or SentencePiece's "▁") begins a new word, and anything else,
// including punctuation, continues the current one. Each word ends where the
// next begins; the last ends at segEnd. Returns nil if the inputs don't line
// up.
func WordsFromTokens(tokens []string, timestamps []float32, segStart, segEnd float64) []Word {
	if len(tokens) == 0 || len(tokens) != len(timestamps) {
		return nil
	}
	var words []Word
	pendingSpace := false
	for i, tok := range tokens {
		// Timestamps are float32 at the model's frame rate (80ms);
		// millisecond precision is plenty and avoids float32 noise.
		at := math.Round((segStart+float64(timestamps[i]))*1000) / 1000
		trimmed := strings.TrimLeft(tok, " ▁")
		startsWord := len(words) == 0 || pendingSpace || len(trimmed) < len(tok)
		if trimmed == "" {
			pendingSpace = true // a bare space token: the next token starts a word
			continue
		}
		pendingSpace = false
		// Punctuation attaches to the word before it, unless there is none
		// yet (an utterance can start with a stray "." or "-").
		if len(words) == 0 || (startsWord && !isPunctuation(trimmed)) {
			words = append(words, Word{Text: trimmed, Start: at})
			continue
		}
		words[len(words)-1].Text += trimmed
	}
	for i := range words {
		if i+1 < len(words) {
			words[i].End = words[i+1].Start
		} else {
			words[i].End = max(segEnd, words[i].Start)
		}
	}
	return words
}

// isPunctuation reports whether a token that starts with a space is really
// trailing punctuation (" ," " ?") rather than a new word. Quotes aren't
// included: a spaced " 'cause" starts a word.
func isPunctuation(s string) bool {
	return strings.Trim(s, ".,!?;:%)") == ""
}

// minRunSeconds is the shortest stretch of words SplitByDiarization will
// give to a different speaker than its neighbors. Diarization boundaries
// wobble by a few hundred milliseconds, so a shorter run is more likely a
// boundary error than a real interjection.
const minRunSeconds = 0.25

// SplitByDiarization labels segments from diarization like
// relabelByDiarization, but splits a segment with word timings wherever
// diarization changes speaker within it, so a quick "Right." from someone
// else inside one utterance becomes its own line with its own speaker.
// Segments without word timings, or not diarizable, keep one label. Returns
// the new segment list and how many segments it labeled.
func SplitByDiarization(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string) ([]Segment, int) {
	assigned, labels := diarizationLabels(segs, diar, speakerMap)
	var out []Segment
	count := 0
	for i, seg := range segs {
		if assigned[i] < 0 {
			out = append(out, seg)
			continue
		}
		runs := speakerRuns(seg.Words, diar, assigned[i])
		if len(runs) <= 1 {
			seg.Speaker = labels[assigned[i]]
			if len(runs) == 1 {
				seg.Speaker = labels[runs[0].speaker]
			}
			out = append(out, seg)
			count++
			continue
		}
		for k, r := range runs {
			part := seg
			part.Words = r.words
			part.Text = joinWords(r.words)
			part.StartTime, part.EndTime = r.words[0].Start, r.words[len(r.words)-1].End
			part.Speaker = labels[r.speaker]
			if k > 0 {
				part.ID = fmt.Sprintf("%s.%d", seg.ID, k+1)
			}
			out = append(out, part)
			count++
		}
	}
	return out, count
}

type speakerRun struct {
	speaker int
	words   []Word
}

// speakerRuns gives each word the diarization speaker talking at its
// start (the shortest matching turn when turns overlap, since that's the
// one cutting in), fills gaps from neighboring words (fallback when none
// is known), and groups consecutive same-speaker words, absorbing runs
// shorter than minRunSeconds into the run before them.
func speakerRuns(words []Word, diar []DiarizeSegment, fallback int) []speakerRun {
	if len(words) == 0 {
		return nil
	}
	spk := make([]int, len(words))
	for i, w := range words {
		spk[i] = -1
		at := w.Start + min(0.1, (w.End-w.Start)/2)
		best := -1.0
		for _, d := range diar {
			if d.Start <= at && at < d.End && (best < 0 || d.End-d.Start < best) {
				spk[i], best = d.Speaker, d.End-d.Start
			}
		}
	}
	for i := range spk {
		if spk[i] < 0 && i > 0 {
			spk[i] = spk[i-1]
		}
	}
	for i := len(spk) - 1; i >= 0; i-- {
		if spk[i] < 0 {
			if i+1 < len(spk) {
				spk[i] = spk[i+1]
			} else {
				spk[i] = fallback
			}
		}
	}

	var runs []speakerRun
	for i, w := range words {
		if len(runs) > 0 && runs[len(runs)-1].speaker == spk[i] {
			runs[len(runs)-1].words = append(runs[len(runs)-1].words, w)
			continue
		}
		runs = append(runs, speakerRun{speaker: spk[i], words: []Word{w}})
	}

	// Absorb runs too short to trust, then merge neighbors that end up
	// with the same speaker.
	var merged []speakerRun
	for _, r := range runs {
		d := r.words[len(r.words)-1].End - r.words[0].Start
		if len(merged) > 0 && (d < minRunSeconds || merged[len(merged)-1].speaker == r.speaker) {
			merged[len(merged)-1].words = append(merged[len(merged)-1].words, r.words...)
			continue
		}
		merged = append(merged, r)
	}
	if len(merged) > 1 {
		first := merged[0]
		if d := first.words[len(first.words)-1].End - first.words[0].Start; d < minRunSeconds {
			merged[1].words = append(append([]Word(nil), first.words...), merged[1].words...)
			merged = merged[1:]
		}
	}
	return merged
}

func joinWords(ws []Word) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.Text
	}
	return strings.Join(parts, " ")
}

// SpreadWords estimates word timings for text spoken from start to end,
// for models that return text without them: each word gets a share of the
// span in proportion to its length (plus one for the gap after it), so a
// line can still be split where the speaker changes (SplitByDiarization),
// at about the right word. Returns nil for empty text.
func SpreadWords(text string, start, end float64) []Word {
	fields := strings.Fields(text)
	if len(fields) == 0 || end <= start {
		return nil
	}
	total := 0
	for _, f := range fields {
		total += len([]rune(f)) + 1
	}
	words := make([]Word, len(fields))
	at := start
	for i, f := range fields {
		d := (end - start) * float64(len([]rune(f))+1) / float64(total)
		words[i] = Word{Text: f, Start: math.Round(at*1000) / 1000, End: math.Round((at+d)*1000) / 1000}
		at += d
	}
	words[len(words)-1].End = end
	return words
}
