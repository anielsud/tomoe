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

	// Reference speaker per frame (-1: none, or inside a collar).
	refAt := make([]int, n)
	for i := range refAt {
		refAt[i] = -1
	}
	for _, t := range ref.Turns {
		for f := frame(t.Start); f < frame(t.End) && f < n; f++ {
			refAt[f] = refIdx[t.Speaker]
		}
	}
	for i := 1; i < len(ref.Turns); i++ {
		if ref.Turns[i].Speaker == ref.Turns[i-1].Speaker {
			continue
		}
		b := ref.Turns[i].Start
		for f := frame(b - collar); f < frame(b+collar) && f < n; f++ {
			if f >= 0 {
				refAt[f] = -1
			}
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
		if refAt[f] < 0 || len(hypAt[f]) == 0 {
			continue
		}
		scored++
		for _, h := range hypAt[f] {
			co[h][refAt[f]]++
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
		}
	}

	correct := 0
	for f := 0; f < n; f++ {
		if refAt[f] < 0 || len(hypAt[f]) == 0 {
			continue
		}
		for _, h := range hypAt[f] {
			if assign[h] == refAt[f] {
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
		found := 0
		for _, w := range words {
			if pool[w] > 0 {
				pool[w]--
				found++
			}
		}
		if 2*found >= len(words) {
			s.Found++
			if mapping[bestSpeaker] == t.Speaker {
				s.RightSpeaker++
			}
		}
	}
	return s
}
