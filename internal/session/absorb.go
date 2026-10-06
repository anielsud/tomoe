package session

import (
	"sort"
	"strings"
)

// AbsorbSmallSpeakers gives the lines of every diarized speaker who says
// fewer than minWords words in the whole meeting to a neighbouring
// speaker: the previous line's if it ended within absorbGap seconds, else
// the next line's, else the previous line's. Speakers keep(label) reports
// true for (a name the user gave) are left alone, as are mic lines. It
// returns how many lines changed; minWords <= 0 changes nothing.
//
// Such speakers are diarization flickers, not people: a piece split off
// someone's sentence or a short backchannel the fingerprints couldn't
// place, each shown as its own "Person N" line (5–21 per meeting in ten
// recorded meetings, even in 1:1s). Absorbing speakers under 15–30 words
// never lowered accuracy on four meetings with a reference and raised it
// by up to half a point; at 60 words it swallowed a real person who said
// 43 (docs/speaker-attribution-research.md, "Ten more meetings").
func AbsorbSmallSpeakers(segs []Segment, minWords int, keep func(label string) bool) int {
	if minWords <= 0 {
		return 0
	}
	var order []int
	words := map[string]int{}
	for i, s := range segs {
		if !diarizable(s) {
			continue
		}
		order = append(order, i)
		words[s.Speaker] += len(strings.Fields(s.Text))
	}
	small := func(label string) bool {
		return words[label] < minWords && (keep == nil || !keep(label))
	}
	sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].StartTime < segs[order[b]].StartTime })

	// Decide every line from the labels as they were, so one absorbed line
	// doesn't become another's neighbour.
	to := map[int]string{}
	for k, i := range order {
		s := segs[i]
		if !small(s.Speaker) {
			continue
		}
		prev, next := -1, -1
		for j := k - 1; j >= 0; j-- {
			if c := segs[order[j]]; !small(c.Speaker) && c.EndTime <= s.StartTime+0.01 {
				prev = order[j]
				break
			}
		}
		for j := k + 1; j < len(order); j++ {
			if c := segs[order[j]]; !small(c.Speaker) && c.StartTime >= s.EndTime-0.01 {
				next = order[j]
				break
			}
		}
		switch {
		case prev >= 0 && s.StartTime-segs[prev].EndTime < absorbGap:
			to[i] = segs[prev].Speaker
		case next >= 0:
			to[i] = segs[next].Speaker
		case prev >= 0:
			to[i] = segs[prev].Speaker
		}
	}
	for i, label := range to {
		segs[i].Speaker = label
	}
	return len(to)
}

// absorbGap is how recently the previous line must have ended (seconds)
// for an absorbed line to continue it rather than join the next one.
const absorbGap = 2.0
