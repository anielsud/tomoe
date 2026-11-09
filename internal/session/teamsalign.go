package session

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// TeamsAlignParams are the rules for naming a session's speakers from the
// Teams transcript of the same meeting (AlignTeams).
//
// A speaker label is given a Teams name when that name covers a clear
// majority of the label's speech that Teams turns overlap:
//
//   - at least MinSeconds of overlap with the name, so a label heard for a
//     few lines is never named from a turn boundary's slop;
//   - at least MinShare of the label's overlapped time, so a label that
//     mixes two people (one cluster for a room, or two similar voices) is
//     left alone rather than given one of their names;
//   - at least MinRatio times the runner-up name's overlap, so 60/40 or
//     55/45 splits between two names never pass even when the rest is
//     noise from a third.
//
// One Teams name may name several labels, since diarization often splits
// one person in two, but only when the evidence is overwhelming: the label
// with the most overlap with the name gets it on the rules above; any
// other label needs OverwhelmingShare of its time to be that name. A label
// that mixes the name with someone else is then left unnamed, instead of
// making two speakers look like one person.
type TeamsAlignParams struct {
	MinSeconds        float64
	MinShare          float64
	MinRatio          float64
	OverwhelmingShare float64
	// MaxShift and ShiftStep bound the search for the clock offset between
	// the Teams transcript and the recording (seconds). MinAnchors is how
	// many word sequences said once in both transcripts must agree on an
	// offset for it to be taken from the text (see textShift); with fewer,
	// it is found from the speakers instead.
	MaxShift   float64
	ShiftStep  float64
	MinAnchors int
	// RequireTextMatch proposes no names unless the text placed the clock
	// (MinAnchors sequences agreed). On saved meetings, a transcript whose
	// text doesn't line up with the recording's is another meeting's (or
	// covers another part of the day): its names would be guesses.
	RequireTextMatch bool
}

// DefaultTeamsAlignParams are the rules `tomoe session teams-align` uses.
func DefaultTeamsAlignParams() TeamsAlignParams {
	return TeamsAlignParams{
		MinSeconds:        30,
		MinShare:          0.6,
		MinRatio:          2,
		OverwhelmingShare: 0.8,
		MaxShift:          300,
		ShiftStep:         1,
		MinAnchors:        20,
		RequireTextMatch:  true,
	}
}

// NameOverlap is how long a Teams name's turns overlapped a label's lines.
type NameOverlap struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
}

// Reasons a label gets no Teams name (TeamsLabel.Reason).
const (
	ReasonNoOverlap   = "no overlap"         // no Teams turn overlaps its lines
	ReasonTooLittle   = "too little overlap" // under MinSeconds with its best name
	ReasonNoMajority  = "no clear majority"  // best name under MinShare
	ReasonCloseSecond = "runner-up too close"
	ReasonNameTaken   = "name taken" // another label has the name, and this one isn't overwhelmingly it
	ReasonNoTextMatch = "text doesn't match the recording"
)

// TeamsLabel is what the Teams transcript says about one speaker label.
type TeamsLabel struct {
	Label string `json:"label"`
	// Speech is the label's lines' total length; Overlapped the part of it
	// Teams turns overlap (counted once per overlapping turn).
	Speech     float64       `json:"speech"`
	Overlapped float64       `json:"overlapped"`
	Names      []NameOverlap `json:"names,omitempty"` // most overlap first
	// Proposed is the Teams name for the label ("" for none, with Reason),
	// Share its part of Overlapped.
	Proposed string  `json:"proposed,omitempty"`
	Share    float64 `json:"share"`
	Reason   string  `json:"reason,omitempty"`
	// TakenBy is the label that has the name, for ReasonNameTaken.
	TakenBy string `json:"taken_by,omitempty"`
}

// TeamsAlignment is AlignTeams' result.
type TeamsAlignment struct {
	// Shift is the seconds added to the Teams times to line them up with
	// the recording (positive: Teams' clock is behind the recording's).
	// AtEdge means the best shift was at the search's limit, so the true
	// offset may be larger.
	Shift  float64 `json:"shift"`
	AtEdge bool    `json:"at_edge,omitempty"`
	// ShiftFrom is how the shift was found: ShiftFromText (Anchors word
	// sequences agreed on it) or ShiftFromSpeakers.
	ShiftFrom string `json:"shift_from"`
	Anchors   int    `json:"anchors"`
	// Agreement is the seconds of speech whose label's best Teams name
	// matches the turn heard at the best shift; AgreementAtZero the same
	// with no shift. Overlapped is all the speech Teams turns overlap.
	Agreement       float64      `json:"agreement"`
	AgreementAtZero float64      `json:"agreement_at_zero"`
	Overlapped      float64      `json:"overlapped"`
	Labels          []TeamsLabel `json:"labels"` // most speech first
	// UnusedNames are Teams names heard during the recording that no
	// label was given.
	UnusedNames []NameOverlap `json:"unused_names,omitempty"`
}

// AlignTeams lines a session's lines (segs, session seconds) up with a
// Teams transcript's turns of the same meeting (spans, on the session's
// clock; see TeamsTranscript.Spans), finds the clock offset between the
// two, and proposes a Teams name for each speaker label (see
// TeamsAlignParams).
//
// The offset is found from the text when it can be: words said once in
// both transcripts, in the same three-word sequence, are heard at times
// that differ by the offset (see textShift). Otherwise it is the shift,
// within ±MaxShift in ShiftStep steps, that maximizes agreement: the
// speech time where a label's most-overlapping Teams name is the one
// talking. A wrong shift spreads every label over several names; the
// right one concentrates each on its person. That needs several labels to
// work: with only "You" and one other, every shift looks about as good.
// Ties go to the smaller shift.
func AlignTeams(segs []Segment, spans []TeamsSpan, p TeamsAlignParams) TeamsAlignment {
	labelIdx := map[string]int{}
	var labels []string
	speech := map[string]float64{}
	type line struct {
		label      int
		start, end float64
	}
	var lines []line
	for _, s := range segs {
		if s.Speaker == "" || s.EndTime <= s.StartTime || s.Status == StatusRemoved {
			continue
		}
		i, ok := labelIdx[s.Speaker]
		if !ok {
			i = len(labels)
			labelIdx[s.Speaker] = i
			labels = append(labels, s.Speaker)
		}
		lines = append(lines, line{i, s.StartTime, s.EndTime})
		speech[s.Speaker] += s.EndTime - s.StartTime
	}
	nameIdx := map[string]int{}
	var names []string
	type turn struct {
		name       int
		start, end float64
	}
	turns := make([]turn, 0, len(spans))
	maxLen := 0.0
	for _, sp := range spans {
		if sp.End <= sp.Start {
			continue
		}
		i, ok := nameIdx[sp.Speaker]
		if !ok {
			i = len(names)
			nameIdx[sp.Speaker] = i
			names = append(names, sp.Speaker)
		}
		turns = append(turns, turn{i, sp.Start, sp.End})
		maxLen = math.Max(maxLen, sp.End-sp.Start)
	}
	sort.SliceStable(turns, func(a, b int) bool { return turns[a].start < turns[b].start })

	// overlap is the label x name matrix of overlapped seconds with the
	// Teams turns moved by shift.
	overlap := func(shift float64) [][]float64 {
		m := make([][]float64, len(labels))
		for i := range m {
			m[i] = make([]float64, len(names))
		}
		for _, l := range lines {
			// Turns are at most maxLen long, so none starting before
			// l.start-maxLen reaches the line.
			from := sort.Search(len(turns), func(k int) bool { return turns[k].start+shift >= l.start-maxLen })
			for k := from; k < len(turns) && turns[k].start+shift < l.end; k++ {
				if o := math.Min(l.end, turns[k].end+shift) - math.Max(l.start, turns[k].start+shift); o > 0 {
					m[l.label][turns[k].name] += o
				}
			}
		}
		return m
	}
	agreement := func(m [][]float64) float64 {
		total := 0.0
		for _, row := range m {
			best := 0.0
			for _, v := range row {
				best = math.Max(best, v)
			}
			total += best
		}
		return total
	}

	var out TeamsAlignment
	zero := overlap(0)
	out.AgreementAtZero = agreement(zero)
	step := p.ShiftStep
	if step <= 0 {
		step = 1
	}
	steps := int(math.Floor(p.MaxShift/step + 1e-9))
	edge := float64(steps) * step
	if shift, anchors := textShift(segs, spans, p.MaxShift); anchors >= max(p.MinAnchors, 1) {
		out.Shift, out.ShiftFrom, out.Anchors = shift, ShiftFromText, anchors
		out.AtEdge = steps > 0 && math.Abs(shift) >= edge-step
	} else {
		best, bestShift := out.AgreementAtZero, 0.0
		// Outward from zero, so a tie keeps the smaller shift.
		for k := 1; k <= steps; k++ {
			for _, shift := range []float64{float64(k) * step, -float64(k) * step} {
				if a := agreement(overlap(shift)); a > best+1e-9 {
					best, bestShift = a, shift
				}
			}
		}
		out.Shift, out.ShiftFrom, out.Anchors = bestShift, ShiftFromSpeakers, anchors
		out.AtEdge = steps > 0 && math.Abs(bestShift) >= edge-1e-9
	}
	m := zero
	if out.Shift != 0 {
		m = overlap(out.Shift)
	}
	out.Agreement = agreement(m)

	used := map[string]bool{}
	for i, label := range labels {
		tl := TeamsLabel{Label: label, Speech: speech[label]}
		for j, name := range names {
			if v := m[i][j]; v > 0 {
				tl.Names = append(tl.Names, NameOverlap{name, v})
				tl.Overlapped += v
			}
		}
		sort.SliceStable(tl.Names, func(a, b int) bool { return tl.Names[a].Seconds > tl.Names[b].Seconds })
		out.Overlapped += tl.Overlapped
		switch {
		case len(tl.Names) == 0:
			tl.Reason = ReasonNoOverlap
		default:
			top := tl.Names[0]
			tl.Share = top.Seconds / tl.Overlapped
			second := 0.0
			if len(tl.Names) > 1 {
				second = tl.Names[1].Seconds
			}
			switch {
			case top.Seconds < p.MinSeconds:
				tl.Reason = ReasonTooLittle
			case tl.Share < p.MinShare:
				tl.Reason = ReasonNoMajority
			case top.Seconds < p.MinRatio*second:
				tl.Reason = ReasonCloseSecond
			default:
				tl.Proposed = top.Name
			}
		}
		out.Labels = append(out.Labels, tl)
	}

	if p.RequireTextMatch && out.ShiftFrom != ShiftFromText {
		for i := range out.Labels {
			if out.Labels[i].Proposed != "" {
				out.Labels[i].Proposed, out.Labels[i].Reason = "", ReasonNoTextMatch
			}
		}
	}

	// One name, one label, unless the evidence is overwhelming: the label
	// with the most overlap keeps the name.
	byName := map[string][]int{}
	for i, tl := range out.Labels {
		if tl.Proposed != "" {
			byName[tl.Proposed] = append(byName[tl.Proposed], i)
		}
	}
	for _, idx := range byName {
		sort.SliceStable(idx, func(a, b int) bool {
			return out.Labels[idx[a]].Names[0].Seconds > out.Labels[idx[b]].Names[0].Seconds
		})
		owner := out.Labels[idx[0]].Label
		for _, i := range idx[1:] {
			if tl := &out.Labels[i]; tl.Share < p.OverwhelmingShare {
				tl.Proposed, tl.Reason, tl.TakenBy = "", ReasonNameTaken, owner
			}
		}
	}
	for _, tl := range out.Labels {
		if tl.Proposed != "" {
			used[tl.Proposed] = true
		}
	}
	heard := make([]float64, len(names))
	for i := range m {
		for j, v := range m[i] {
			heard[j] += v
		}
	}
	for j, name := range names {
		if heard[j] > 0 && !used[name] {
			out.UnusedNames = append(out.UnusedNames, NameOverlap{name, heard[j]})
		}
	}
	sort.SliceStable(out.UnusedNames, func(a, b int) bool { return out.UnusedNames[a].Seconds > out.UnusedNames[b].Seconds })
	sort.SliceStable(out.Labels, func(a, b int) bool { return out.Labels[a].Speech > out.Labels[b].Speech })
	return out
}

// How a TeamsAlignment's shift was found.
const (
	ShiftFromText     = "text"
	ShiftFromSpeakers = "speakers"
)

// timedWord is a word and when it was heard (session seconds).
type timedWord struct {
	w string
	t float64
}

// textWords splits text into lowercase words of letters and digits.
func textWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// spread times text's words evenly over start..end.
func spread(text string, start, end float64) []timedWord {
	ws := textWords(text)
	out := make([]timedWord, len(ws))
	for i, w := range ws {
		out[i] = timedWord{w, start + (end-start)*(float64(i)+0.5)/float64(len(ws))}
	}
	return out
}

// uniqueTrigrams maps each three-word sequence said exactly once to when
// its first word was heard.
func uniqueTrigrams(ws []timedWord) map[string]float64 {
	at := map[string]float64{}
	seen := map[string]int{}
	for i := 0; i+2 < len(ws); i++ {
		k := ws[i].w + " " + ws[i+1].w + " " + ws[i+2].w
		seen[k]++
		at[k] = ws[i].t
	}
	for k, n := range seen {
		if n != 1 {
			delete(at, k)
		}
	}
	return at
}

// textShift finds the clock offset between the recording's lines and the
// Teams turns from what was said: every three-word sequence said exactly
// once in each gives the difference between when the recording heard it
// and when Teams put it (Teams' word times are spread evenly over each
// turn, so each difference is rough). The offset is where most
// differences within ±maxShift fall (a 5 s window), refined to their
// median. Returns the offset and how many sequences agreed on it (within
// 2.5 s of it); 0 when none.
func textShift(segs []Segment, spans []TeamsSpan, maxShift float64) (float64, int) {
	var rec []timedWord
	sorted := append([]Segment(nil), segs...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].StartTime < sorted[b].StartTime })
	for _, s := range sorted {
		if s.Status == StatusRemoved {
			continue
		}
		if len(s.Words) > 0 {
			for _, w := range s.Words {
				for _, part := range textWords(w.Text) {
					rec = append(rec, timedWord{part, w.Start})
				}
			}
			continue
		}
		rec = append(rec, spread(s.Text, s.StartTime, s.EndTime)...)
	}
	var teams []timedWord
	for _, sp := range spans {
		teams = append(teams, spread(sp.Text, sp.Start, sp.End)...)
	}
	a, b := uniqueTrigrams(rec), uniqueTrigrams(teams)
	var diffs []float64
	for k, tr := range a {
		if tt, ok := b[k]; ok {
			if d := tr - tt; math.Abs(d) <= maxShift+2.5 {
				diffs = append(diffs, d)
			}
		}
	}
	if len(diffs) == 0 {
		return 0, 0
	}
	sort.Float64s(diffs)
	// The 5 s window holding the most differences.
	bestLo, bestN := 0, 0
	hi := 0
	for lo := range diffs {
		for hi < len(diffs) && diffs[hi]-diffs[lo] <= 5 {
			hi++
		}
		if hi-lo > bestN {
			bestLo, bestN = lo, hi-lo
		}
	}
	window := diffs[bestLo : bestLo+bestN]
	shift := window[len(window)/2]
	if len(window)%2 == 0 {
		shift = (window[len(window)/2-1] + window[len(window)/2]) / 2
	}
	shift = math.Max(-maxShift, math.Min(maxShift, shift))
	n := 0
	for _, d := range diffs {
		if math.Abs(d-shift) <= 2.5 {
			n++
		}
	}
	return shift, n
}
