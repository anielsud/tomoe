package diarize

import (
	"sort"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// ShortLineMax is the longest meeting-audio line (seconds) whose speaker
// comes from the meeting window's highlight when the highlight shows one
// person for all of it. A line that short is too little voice to tell
// speakers apart by (a "Thank you" went to the wrong person), while the
// highlight follows who is talking within a second or two.
const ShortLineMax = 1.5

// SoleHint is the one name every hint read while start..end was spoken
// shows (hints shifted back by the ring's lag, plus a quarter second of
// slack either side), or "" when there were none or they disagree.
// hints are in time order.
func SoleHint(hints []Hint, start, end float64) string {
	from, to := start+hintLag-0.25, end+hintLag+0.25
	i := sort.Search(len(hints), func(i int) bool { return hints[i].T >= from })
	name := ""
	for ; i < len(hints) && hints[i].T <= to; i++ {
		h := hints[i]
		if h.Name == "" {
			if len(h.Candidates) > 0 {
				return "" // several lit at once: no single answer
			}
			continue
		}
		if name != "" && !SameName(name, h.Name) {
			return ""
		}
		if name == "" {
			name = h.Name
		}
	}
	return name
}

// ShortLineLabel is the label for a short meeting-audio line the
// highlight names unambiguously: labelOf finds the speaker carrying a
// name. ok is false when the rule doesn't apply.
func ShortLineLabel(seg session.Segment, hints []Hint, labelOf func(name string) (string, bool)) (string, bool) {
	if seg.Source != "monitor" || seg.EndTime-seg.StartTime > ShortLineMax {
		return "", false
	}
	name := SoleHint(hints, seg.StartTime, seg.EndTime)
	if name == "" {
		return "", false
	}
	return labelOf(name)
}

// NameShortLines applies ShortLineLabel to segs in place and reports how
// many lines changed speaker.
func NameShortLines(segs []session.Segment, hints []Hint, labelOf func(name string) (string, bool)) int {
	n := 0
	for i := range segs {
		if label, ok := ShortLineLabel(segs[i], hints, labelOf); ok && label != segs[i].Speaker {
			segs[i].Speaker = label
			n++
		}
	}
	return n
}
