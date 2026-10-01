package eval

import (
	"math"
	"sort"
)

// Labeled is one stretch of hypothesis speech with a speaker label (and,
// for transcript passes, its text). Stretches may overlap each other:
// diarization reports two speakers at once where it detects overlap.
type Labeled struct {
	Start   float64
	End     float64
	Speaker string
	Text    string
}

// frameStep is the timeline resolution speaker scoring works at.
const frameStep = 0.1

// SpeakerScore scores one pass's speaker labels against the reference, over
// the time where that pass detected speech.
type SpeakerScore struct {
	// ScoredSeconds is the time that counted: speech the pass detected,
	// inside a reference turn, away from turn changes (see ScoreSpeakers).
	ScoredSeconds float64 `json:"scored_seconds"`
	// Confusion is the share of scored time given to the wrong person,
	// after mapping each hypothesis label to at most one reference speaker
	// (and vice versa) to maximize agreement. Lower is better; 0 is perfect.
	Confusion float64 `json:"confusion"`
	// Purity is how much of each hypothesis speaker's time belongs to one
	// person (time-weighted): low means voices are mixed together.
	Purity float64 `json:"purity"`
	// Coverage is how much of each person's time lands in one hypothesis
	// speaker (time-weighted): low means a person is split up.
	Coverage    float64 `json:"coverage"`
	HypSpeakers int     `json:"hyp_speakers"`
	RefSpeakers int     `json:"ref_speakers"`
	// RefSpeakersMatched is how many reference speakers got a hypothesis
	// speaker of their own in the mapping: the rest were merged into
	// someone else's (or never heard).
	RefSpeakersMatched int `json:"ref_speakers_matched"`
	// OverlapSeconds is time the pass labeled with two or more speakers at
	// once (only diarization does this).
	OverlapSeconds float64 `json:"overlap_seconds"`
	// Mapping is each hypothesis label's matched reference speaker.
	Mapping map[string]string `json:"mapping"`
}

// ScoreSpeakers scores hyp's speaker labels against ref. collar seconds on
// each side of a change between two different reference speakers aren't
// scored: a Teams export's start times are only accurate to the second.
func ScoreSpeakers(ref *Reference, hyp []Labeled, collar float64) SpeakerScore {
	refSpeakers := ref.Speakers()
	refIdx := map[string]int{}
	for i, s := range refSpeakers {
		refIdx[s] = i
	}
	end := 0.0
	for _, t := range ref.Turns {
		end = math.Max(end, t.End)
	}
	for _, h := range hyp {
		end = math.Max(end, h.End)
	}
	n := int(end/frameStep) + 1

	// Reference speakers per frame: usually one, several during annotated
	// overlap, none in a collar or outside any turn.
	refAt := make([][]int, n)
	for _, t := range ref.Turns {
		for f := frame(t.Start); f < frame(t.End) && f < n; f++ {
			if f >= 0 && !contains(refAt[f], refIdx[t.Speaker]) {
				refAt[f] = append(refAt[f], refIdx[t.Speaker])
			}
		}
	}
	clearCollar := func(b float64) {
		for f := frame(b - collar); f < frame(b+collar) && f < n; f++ {
			if f >= 0 {
				refAt[f] = nil
			}
		}
	}
	for i := range ref.Turns {
		if i > 0 && ref.Turns[i].Speaker != ref.Turns[i-1].Speaker {
			clearCollar(ref.Turns[i].Start)
		}
		if ref.Overlapping(i) {
			clearCollar(ref.Turns[i].End) // an estimated end
		}
	}

	// Hypothesis speakers per frame.
	var hypLabels []string
	hypIdx := map[string]int{}
	hypAt := make([][]int, n)
	for _, h := range hyp {
		idx, ok := hypIdx[h.Speaker]
		if !ok {
			idx = len(hypLabels)
			hypIdx[h.Speaker] = idx
			hypLabels = append(hypLabels, h.Speaker)
		}
		for f := frame(h.Start); f < frame(h.End) && f < n; f++ {
			if f >= 0 && !contains(hypAt[f], idx) {
				hypAt[f] = append(hypAt[f], idx)
			}
		}
	}

	co := make([][]float64, len(hypLabels))
	for i := range co {
		co[i] = make([]float64, len(refSpeakers))
	}
	score := SpeakerScore{RefSpeakers: len(refSpeakers), HypSpeakers: len(hypLabels), Mapping: map[string]string{}}
	scored := 0
	for f := 0; f < n; f++ {
		if len(hypAt[f]) >= 2 {
			score.OverlapSeconds += frameStep
		}
		if len(refAt[f]) == 0 || len(hypAt[f]) == 0 {
			continue
		}
		scored++
		for _, h := range hypAt[f] {
			for _, r := range refAt[f] {
				co[h][r]++
			}
		}
	}
	score.ScoredSeconds = float64(scored) * frameStep
	if scored == 0 {
		return score
	}

	assign := maxWeightMatching(co)
	for h, r := range assign {
		if r >= 0 && co[h][r] > 0 {
			score.Mapping[hypLabels[h]] = refSpeakers[r]
			score.RefSpeakersMatched++
		}
	}

	correct := 0
	for f := 0; f < n; f++ {
		if len(refAt[f]) == 0 || len(hypAt[f]) == 0 {
			continue
		}
		for _, h := range hypAt[f] {
			if assign[h] >= 0 && contains(refAt[f], assign[h]) {
				correct++
				break
			}
		}
	}
	score.Confusion = 1 - float64(correct)/float64(scored)

	var total, pure, covered float64
	for h := range co {
		best := 0.0
		for r := range co[h] {
			total += co[h][r]
			best = math.Max(best, co[h][r])
		}
		pure += best
	}
	for r := range refSpeakers {
		best := 0.0
		for h := range co {
			best = math.Max(best, co[h][r])
		}
		covered += best
	}
	score.Purity = pure / total
	score.Coverage = covered / total
	return score
}

func frame(t float64) int { return int(math.Floor(t/frameStep + 1e-9)) }

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// DetectionScore is how much of the reference the pipeline heard at all.
type DetectionScore struct {
	Turns      int `json:"turns"`
	TurnsHeard int `json:"turns_heard"`
	Words      int `json:"words"`
	WordsHeard int `json:"words_heard"` // words in heard turns
}

// ScoreDetection counts a reference turn as heard if any hypothesis speech
// overlaps it. A turn's own length is estimated from its word count, since
// its End includes any pause before the next turn.
func ScoreDetection(ref *Reference, hyp []Labeled, slack float64) DetectionScore {
	sorted := append([]Labeled(nil), hyp...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var s DetectionScore
	for _, t := range ref.Turns {
		words := len(Words(t.Text))
		s.Turns++
		s.Words += words
		spoken := math.Min(t.End, t.Start+math.Max(1, float64(words)/2.5))
		if overlapsAny(sorted, t.Start-slack, spoken+slack) {
			s.TurnsHeard++
			s.WordsHeard += words
		}
	}
	return s
}

func overlapsAny(sorted []Labeled, start, end float64) bool {
	for _, h := range sorted {
		if h.Start >= end {
			return false
		}
		if h.End > start {
			return true
		}
	}
	return false
}

// SpeakerAttributedErrors is the word error count for "who said what":
// each reference speaker's words are aligned against the words of the
// hypothesis speakers mapped to them (see SpeakerScore.Mapping), so a word
// transcribed right but given to the wrong person counts as an error.
// Words from unmapped hypothesis speakers count as insertions.
func SpeakerAttributedErrors(ref *Reference, hyp []Labeled, mapping map[string]string) ErrorCounts {
	refWords := map[string][]string{}
	for _, t := range ref.Turns {
		refWords[t.Speaker] = append(refWords[t.Speaker], Words(t.Text)...)
	}
	sorted := append([]Labeled(nil), hyp...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	hypWords := map[string][]string{}
	var total ErrorCounts
	for _, h := range sorted {
		if r, ok := mapping[h.Speaker]; ok {
			hypWords[r] = append(hypWords[r], Words(h.Text)...)
		} else {
			total.Insertions += len(Words(h.Text))
		}
	}
	for _, spk := range ref.Speakers() {
		total = total.Add(Align(refWords[spk], hypWords[spk]))
	}
	return total
}

// ExchangeScore scores quick exchanges: short reference turns squeezed
// between other speakers ("Yeah." "Right, exactly." mid-discussion). A
// Teams export can't mark true overlap, so this is the closest proxy for
// cross-talk it supports.
type ExchangeScore struct {
	Turns int `json:"turns"`
	// Found counts turns with at least half their words present in the
	// hypothesis text nearby, and RightSpeaker those among them whose
	// best-matching hypothesis line has the right speaker.
	Found        int `json:"found"`
	RightSpeaker int `json:"right_speaker"`
}

// ScoreQuickExchanges finds reference turns of at most maxWords words,
// lasting at most maxSeconds before the next turn, with a different speaker
// on both sides, and scores how many the hypothesis captured (text within
// window seconds) and attributed correctly (via mapping).
func ScoreQuickExchanges(ref *Reference, hyp []Labeled, mapping map[string]string, maxWords int, maxSeconds, window float64) ExchangeScore {
	var s ExchangeScore
	for i := 1; i+1 < len(ref.Turns); i++ {
		t := ref.Turns[i]
		words := Words(t.Text)
		if len(words) == 0 || len(words) > maxWords || t.End-t.Start > maxSeconds ||
			ref.Turns[i-1].Speaker == t.Speaker || ref.Turns[i+1].Speaker == t.Speaker {
			continue
		}
		s.Turns++
		found, right := turnCaptured(t, hyp, mapping, window)
		if found {
			s.Found++
			if right {
				s.RightSpeaker++
			}
		}
	}
	return s
}

// turnCaptured reports whether hypothesis text within window seconds of
// turn t contains at least half its words (found), and whether the
// hypothesis line matching most of them has t's speaker (right).
func turnCaptured(t Turn, hyp []Labeled, mapping map[string]string, window float64) (found, right bool) {
	words := Words(t.Text)
	if len(words) == 0 {
		return false, false
	}
	bestHits, bestSpeaker := 0, ""
	pool := map[string]int{}
	for _, h := range hyp {
		if h.End < t.Start-window || h.Start > t.End+window {
			continue
		}
		hits := 0
		local := map[string]int{}
		for _, w := range Words(h.Text) {
			pool[w]++
			local[w]++
		}
		for _, w := range words {
			if local[w] > 0 {
				local[w]--
				hits++
			}
		}
		if hits > bestHits {
			bestHits, bestSpeaker = hits, h.Speaker
		}
	}
	got := 0
	for _, w := range words {
		if pool[w] > 0 {
			pool[w]--
			got++
		}
	}
	if 2*got < len(words) {
		return false, false
	}
	return true, mapping[bestSpeaker] == t.Speaker
}

// OverlapScore scores annotated simultaneous speech: reference turns that
// share a start time (see ParseTeamsTranscript).
type OverlapScore struct {
	// Interjections are the shorter turns in each overlapping group: the
	// voice cutting in while someone else keeps talking.
	Interjections int `json:"interjections"`
	// Found and RightSpeaker are how many interjections the pass
	// transcribed, and gave to the right person (as in ExchangeScore).
	Found        int `json:"found"`
	RightSpeaker int `json:"right_speaker"`
	// Seconds is the annotated overlap time, and DetectedSeconds how much
	// of it the pass labeled with two or more speakers at once.
	Seconds         float64 `json:"seconds"`
	DetectedSeconds float64 `json:"detected_seconds"`
}

// ScoreAnnotatedOverlaps scores how a pass handled each annotated overlap.
func ScoreAnnotatedOverlaps(ref *Reference, hyp []Labeled, mapping map[string]string, window float64) OverlapScore {
	var s OverlapScore
	for i := 0; i < len(ref.Turns); {
		j := i
		for j < len(ref.Turns) && ref.Turns[j].Start == ref.Turns[i].Start {
			j++
		}
		if j-i > 1 {
			longest := i
			for k := i; k < j; k++ {
				if len(Words(ref.Turns[k].Text)) > len(Words(ref.Turns[longest].Text)) {
					longest = k
				}
			}
			for k := i; k < j; k++ {
				if k == longest {
					continue
				}
				t := ref.Turns[k]
				s.Interjections++
				s.Seconds += t.End - t.Start
				if found, right := turnCaptured(t, hyp, mapping, window); found {
					s.Found++
					if right {
						s.RightSpeaker++
					}
				}
				for f := frame(t.Start); f < frame(t.End); f++ {
					at := float64(f) * frameStep
					speakers := map[string]bool{}
					for _, h := range hyp {
						if h.Start <= at && at < h.End {
							speakers[h.Speaker] = true
						}
					}
					if len(speakers) >= 2 {
						s.DetectedSeconds += frameStep
					}
				}
			}
		}
		i = j
	}
	return s
}

// TurnBucket groups reference turns by length, in words.
type TurnBucket struct {
	Label    string `json:"label"`
	MinWords int    `json:"min_words"`
	MaxWords int    `json:"max_words"` // 0 = no upper limit
	Words    int    `json:"words"`
	Correct  int    `json:"correct"`
}

// Accuracy is the share of the bucket's words with the right speaker.
func (b TurnBucket) Accuracy() float64 {
	if b.Words == 0 {
		return 0
	}
	return float64(b.Correct) / float64(b.Words)
}

// DefaultTurnBuckets are the turn-length groups word accuracy is broken
// down by: interjections, short replies, normal turns and monologues.
func DefaultTurnBuckets() []TurnBucket {
	return []TurnBucket{
		{Label: "1-3 words", MinWords: 1, MaxWords: 3},
		{Label: "4-15 words", MinWords: 4, MaxWords: 15},
		{Label: "16-60 words", MinWords: 16, MaxWords: 60},
		{Label: "61+ words", MinWords: 61},
	}
}

// WordSpeakerScore is speaker accuracy by word: of the transcribed words
// matched to a reference word, the share whose speaker is the one who said
// that reference word, overall and by the length of its turn.
type WordSpeakerScore struct {
	// Words is how many transcribed words matched a reference word (and so
	// were scored); RefWords is the reference total.
	Words    int `json:"words"`
	RefWords int `json:"ref_words"`
	Correct  int `json:"correct"`
	// Unlabeled counts scored words the pass gave no speaker at all (a
	// diarization gap): wrong, but missed rather than misattributed.
	Unlabeled int          `json:"unlabeled"`
	Buckets   []TurnBucket `json:"buckets"`
}

// Accuracy is the overall share of scored words with the right speaker.
func (s WordSpeakerScore) Accuracy() float64 {
	if s.Words == 0 {
		return 0
	}
	return float64(s.Correct) / float64(s.Words)
}

// WordAlignment pairs a pass's transcribed words with the reference words
// they correspond to, by text, so each transcribed word knows which
// reference turn (and so which speaker) it belongs to. Aligning by text
// rather than by time needs no collar around speaker changes, so short
// turns and interjections are scored too, and during annotated overlap a
// word belongs to whoever actually said it.
//
// Build one per sequence of transcribed words and reuse it for every pass
// that labels those same words (live, final, split, diarization).
type WordAlignment struct {
	ref       *Reference
	turnWords []int // words per reference turn
	// tokenWord[k] is the transcribed word normalized token k came from,
	// and tokenTurn[k] the reference turn it aligned to (-1: none).
	tokenWord []int
	tokenTurn []int
	refTokens int
}

// NewWordAlignment aligns texts (the transcribed words, in order) with the
// reference's words.
func NewWordAlignment(ref *Reference, texts []string) *WordAlignment {
	a := &WordAlignment{ref: ref, turnWords: make([]int, len(ref.Turns))}
	var refToks []string
	var refTurn []int
	for i, t := range ref.Turns {
		ws := Words(t.Text)
		a.turnWords[i] = len(ws)
		for _, w := range ws {
			refToks = append(refToks, w)
			refTurn = append(refTurn, i)
		}
	}
	a.refTokens = len(refToks)
	var hypToks []string
	for i, t := range texts {
		for _, w := range Words(t) {
			hypToks = append(hypToks, w)
			a.tokenWord = append(a.tokenWord, i)
		}
	}
	pairs := alignPairs(refToks, hypToks)
	a.tokenTurn = make([]int, len(hypToks))
	for k, r := range pairs {
		a.tokenTurn[k] = -1
		if r >= 0 {
			a.tokenTurn[k] = refTurn[r]
		}
	}
	return a
}

// Score scores one pass: speakers[i] are the labels the pass gives
// transcribed word i (as passed to NewWordAlignment). A word is right if
// any label maps (via mapping) to the speaker of the reference turn it
// aligned to. Transcribed words that align to no reference word aren't
// scored.
func (a *WordAlignment) Score(speakers [][]string, mapping map[string]string) WordSpeakerScore {
	return a.ScoreWhere(speakers, mapping, nil)
}

// ScoreWhere is Score counting only the transcribed words keep accepts
// (all of them if keep is nil).
func (a *WordAlignment) ScoreWhere(speakers [][]string, mapping map[string]string, keep func(word int) bool) WordSpeakerScore {
	s := WordSpeakerScore{RefWords: a.refTokens, Buckets: DefaultTurnBuckets()}
	for k, turn := range a.tokenTurn {
		if turn < 0 || (keep != nil && !keep(a.tokenWord[k])) {
			continue
		}
		want := a.ref.Turns[turn].Speaker
		right := false
		if w := a.tokenWord[k]; w >= len(speakers) || len(speakers[w]) == 0 {
			s.Unlabeled++
		} else {
			for _, l := range speakers[w] {
				if mapping[l] == want {
					right = true
					break
				}
			}
		}
		s.Words++
		if right {
			s.Correct++
		}
		n := a.turnWords[turn]
		for b := range s.Buckets {
			bk := &s.Buckets[b]
			if n >= bk.MinWords && (bk.MaxWords == 0 || n <= bk.MaxWords) {
				bk.Words++
				if right {
					bk.Correct++
				}
				break
			}
		}
	}
	return s
}

// alignPairs computes the minimum-edit alignment of hyp against ref and
// returns, for each hyp token, the ref token it's paired with (a match or a
// substitution), or -1 for an insertion. Keeps one byte of backtrace per
// cell, so an hour-long meeting (~9k x 9k) needs ~80MB.
func alignPairs(ref, hyp []string) []int {
	n, m := len(ref), len(hyp)
	const (
		diag = iota
		up   // deletion: a ref token with no hyp token
		left // insertion: a hyp token with no ref token
	)
	back := make([]byte, (n+1)*(m+1))
	prev := make([]int32, m+1)
	cur := make([]int32, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = int32(j)
		back[j] = left
	}
	for i := 1; i <= n; i++ {
		cur[0] = int32(i)
		back[i*(m+1)] = up
		for j := 1; j <= m; j++ {
			cost := prev[j-1]
			if ref[i-1] != hyp[j-1] {
				cost++
			}
			best, dir := cost, byte(diag)
			if prev[j]+1 < best {
				best, dir = prev[j]+1, up
			}
			if cur[j-1]+1 < best {
				best, dir = cur[j-1]+1, left
			}
			cur[j] = best
			back[i*(m+1)+j] = dir
		}
		prev, cur = cur, prev
	}
	pairs := make([]int, m)
	i, j := n, m
	for i > 0 || j > 0 {
		switch back[i*(m+1)+j] {
		case diag:
			pairs[j-1] = i - 1
			i, j = i-1, j-1
		case up:
			i--
		default:
			pairs[j-1] = -1
			j--
		}
	}
	return pairs
}

// SpeakersAt returns, for each time, the labels of the stretches covering
// it: how a diarization pass labels a word.
func SpeakersAt(hyp []Labeled, times []float64) [][]string {
	sorted := append([]Labeled(nil), hyp...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	out := make([][]string, len(times))
	for i, t := range times {
		for _, h := range sorted {
			if h.Start > t {
				break
			}
			if t < h.End && !containsString(out[i], h.Speaker) {
				out[i] = append(out[i], h.Speaker)
			}
		}
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
