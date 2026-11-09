package session

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// meeting builds a synthetic meeting: who talks for how long, in order.
// It returns the recording's lines (labelled by label(person, index)) and
// the Teams spans for the same talk, with Teams' clock off by -offset.
func meeting(turns []struct {
	who string
	dur float64
}, label func(who string, i int) string, offset float64) ([]Segment, []TeamsSpan) {
	var segs []Segment
	var spans []TeamsSpan
	t := 3.0
	for i, tr := range turns {
		segs = append(segs, Segment{Speaker: label(tr.who, i), StartTime: t, EndTime: t + tr.dur - 0.4})
		spans = append(spans, TeamsSpan{Speaker: tr.who, Start: t - offset, End: t + tr.dur - offset})
		t += tr.dur
	}
	return segs, spans
}

type talk = struct {
	who string
	dur float64
}

// script repeats a varied pattern so a wrong shift mixes speakers.
func script(n int) []talk {
	durs := []float64{7, 13, 4, 21, 9, 5, 17, 11, 6, 15}
	people := []string{"Ana Lee", "Ben Ode", "Ana Lee", "Cy Park", "Ben Ode", "Cy Park", "Ana Lee", "Ben Ode", "Cy Park", "Ana Lee"}
	var out []talk
	for i := 0; i < n; i++ {
		out = append(out, talk{people[i%len(people)], durs[(i*7)%len(durs)]})
	}
	return out
}

// speakerParams are the default rules for synthetic meetings with no
// text: the clock is placed from the speakers.
func speakerParams() TeamsAlignParams {
	p := DefaultTeamsAlignParams()
	p.RequireTextMatch = false
	return p
}

func find(a TeamsAlignment, label string) TeamsLabel {
	for _, l := range a.Labels {
		if l.Label == label {
			return l
		}
	}
	return TeamsLabel{}
}

func TestAlignTeamsShiftAndNames(t *testing.T) {
	segs, spans := meeting(script(60), func(who string, i int) string {
		return map[string]string{"Ana Lee": "Person 1", "Ben Ode": "Person 2", "Cy Park": "You"}[who]
	}, 7)
	a := AlignTeams(segs, spans, speakerParams())
	if a.Shift != 7 {
		t.Errorf("shift = %v, want 7", a.Shift)
	}
	if a.Agreement <= a.AgreementAtZero {
		t.Errorf("agreement %v not above unshifted %v", a.Agreement, a.AgreementAtZero)
	}
	for label, want := range map[string]string{"Person 1": "Ana Lee", "Person 2": "Ben Ode", "You": "Cy Park"} {
		l := find(a, label)
		if l.Proposed != want || l.Share < 0.95 {
			t.Errorf("%s: proposed %q share %.2f (%s), want %q", label, l.Proposed, l.Share, l.Reason, want)
		}
	}
	if len(a.UnusedNames) != 0 {
		t.Errorf("unused names = %v", a.UnusedNames)
	}
}

func TestAlignTeamsNegativeShiftAndEdge(t *testing.T) {
	segs, spans := meeting(script(60), func(who string, i int) string { return "L " + who }, -12)
	p := speakerParams()
	if a := AlignTeams(segs, spans, p); a.Shift != -12 || a.AtEdge {
		t.Errorf("shift = %v (edge %v), want -12", a.Shift, a.AtEdge)
	}
	p.MaxShift = 5
	if a := AlignTeams(segs, spans, p); !a.AtEdge {
		t.Errorf("shift %v with a 5 s search should be at the edge", a.Shift)
	}
}

func TestAlignTeamsRules(t *testing.T) {
	// Person 3 is Ben and Cy alike (a mixed cluster); Person 4 is a
	// fragment of Ana split off (all Ana); Person 5 is Ana mixed with a
	// little Ben; Person 6 speaks once, briefly.
	segs, spans := meeting(script(80), func(who string, i int) string {
		switch {
		case who == "Ana Lee" && i%8 == 0:
			return "Person 4"
		case who == "Ana Lee" && i%8 == 2:
			return "Person 5"
		case who == "Ben Ode" && i == 1:
			return "Person 5"
		case who == "Ana Lee":
			return "Person 1"
		case i == 78: // Cy Park
			return "Person 6"
		default:
			return "Person 3"
		}
	}, 0)
	p := speakerParams()
	a := AlignTeams(segs, spans, p)
	if a.Shift != 0 {
		t.Errorf("shift = %v, want 0", a.Shift)
	}
	if l := find(a, "Person 1"); l.Proposed != "Ana Lee" {
		t.Errorf("Person 1: %q (%s)", l.Proposed, l.Reason)
	}
	if l := find(a, "Person 3"); l.Proposed != "" || (l.Reason != ReasonNoMajority && l.Reason != ReasonCloseSecond) {
		t.Errorf("Person 3 (mixed): %q (%s), want none for no majority", l.Proposed, l.Reason)
	}
	// All Ana: overwhelming, so it shares Person 1's name.
	if l := find(a, "Person 4"); l.Proposed != "Ana Lee" || l.Share < p.OverwhelmingShare {
		t.Errorf("Person 4 (fragment): %q share %.2f (%s)", l.Proposed, l.Share, l.Reason)
	}
	if l := find(a, "Person 6"); l.Proposed != "" || l.Reason != ReasonTooLittle {
		t.Errorf("Person 6 (brief): %q (%s)", l.Proposed, l.Reason)
	}
	// Ben and Cy were only heard on mixed labels: unused.
	unused := map[string]bool{}
	for _, n := range a.UnusedNames {
		unused[n.Name] = true
	}
	if !unused["Ben Ode"] || !unused["Cy Park"] || unused["Ana Lee"] {
		t.Errorf("unused = %v", a.UnusedNames)
	}
}

func TestAlignTeamsNameTaken(t *testing.T) {
	// Two labels both mostly Ana: the bigger keeps the name; the other is
	// 70% Ana (not overwhelming), so it is left unnamed.
	var segs []Segment
	var spans []TeamsSpan
	add := func(start, end float64, label, who string) {
		segs = append(segs, Segment{Speaker: label, StartTime: start, EndTime: end})
		spans = append(spans, TeamsSpan{Speaker: who, Start: start, End: end})
	}
	add(0, 200, "Person 1", "Ana Lee")
	add(200, 270, "Person 2", "Ana Lee")
	add(270, 300, "Person 2", "Ben Ode")
	p := speakerParams()
	p.MaxShift = 0
	a := AlignTeams(segs, spans, p)
	if l := find(a, "Person 1"); l.Proposed != "Ana Lee" {
		t.Errorf("Person 1: %q (%s)", l.Proposed, l.Reason)
	}
	l := find(a, "Person 2")
	if l.Proposed != "" || l.Reason != ReasonNameTaken || l.TakenBy != "Person 1" {
		t.Errorf("Person 2: %q (%s, by %q), want name taken by Person 1", l.Proposed, l.Reason, l.TakenBy)
	}
}

func TestAlignTeamsCloseSecond(t *testing.T) {
	// 60% / 32% / 8%: a majority, but the runner-up is more than half the
	// leader.
	segs := []Segment{{Speaker: "Person 1", StartTime: 0, EndTime: 100}}
	spans := []TeamsSpan{{Speaker: "Ana Lee", Start: 0, End: 60}, {Speaker: "Ben Ode", Start: 60, End: 92}, {Speaker: "Cy Park", Start: 92, End: 100}}
	p := speakerParams()
	p.MaxShift = 0
	if l := find(AlignTeams(segs, spans, p), "Person 1"); l.Proposed != "" || l.Reason != ReasonCloseSecond {
		t.Errorf("got %q (%s), want runner-up too close", l.Proposed, l.Reason)
	}
}

func TestAlignTeamsEmpty(t *testing.T) {
	a := AlignTeams(nil, nil, speakerParams())
	if a.Shift != 0 || len(a.Labels) != 0 {
		t.Errorf("empty: %+v", a)
	}
	segs := []Segment{{Speaker: "Person 1", StartTime: 0, EndTime: 10}}
	if l := find(AlignTeams(segs, nil, speakerParams()), "Person 1"); l.Reason != ReasonNoOverlap {
		t.Errorf("no turns: %+v", l)
	}
}

func TestAlignTeamsShiftFromText(t *testing.T) {
	// Two people only, so the speakers can't place the clock; the words
	// can. Teams' clock is 37 s behind the recording's.
	vocab := []string{"budget", "roadmap", "launch", "pricing", "hiring", "travel", "review", "quarter", "partner", "vendor", "metric", "target", "deadline", "design", "support"}
	var segs []Segment
	var spans []TeamsSpan
	tm := 5.0
	for i := 0; i < 40; i++ {
		who, label := "Ana Lee", "Person 1"
		if i%2 == 1 {
			who, label = "Ben Ode", "You"
		}
		// Common words repeat across turns; a numbered topic word makes
		// some sequences said only once.
		var w []string
		for j := 0; j < 12; j++ {
			w = append(w, vocab[(i+j)%len(vocab)])
			if j%4 == 0 {
				w = append(w, fmt.Sprintf("item%d", i*12+j))
			}
		}
		text := strings.Join(w, " ")
		dur := 6 + float64(i%5)
		segs = append(segs, Segment{Speaker: label, StartTime: tm, EndTime: tm + dur - 0.3, Text: text})
		spans = append(spans, TeamsSpan{Speaker: who, Start: tm - 37, End: tm + dur - 37, Text: text})
		tm += dur
	}
	a := AlignTeams(segs, spans, DefaultTeamsAlignParams())
	if a.ShiftFrom != ShiftFromText || math.Abs(a.Shift-37) > 1.5 {
		t.Errorf("shift %v from %s (%d anchors), want 37 from text", a.Shift, a.ShiftFrom, a.Anchors)
	}
	if l := find(a, "Person 1"); l.Proposed != "Ana Lee" {
		t.Errorf("Person 1: %q (%s)", l.Proposed, l.Reason)
	}
}

func TestAlignTeamsRequiresTextMatch(t *testing.T) {
	// The speakers line up but the words don't: another meeting's
	// transcript, or the wrong part of it. No names.
	segs, spans := meeting(script(60), func(who string, i int) string { return "L " + who }, 0)
	for i := range segs {
		segs[i].Text = fmt.Sprintf("recording words %d here", i)
		spans[i].Text = fmt.Sprintf("teams words %d there", i)
	}
	a := AlignTeams(segs, spans, DefaultTeamsAlignParams())
	if a.ShiftFrom != ShiftFromSpeakers {
		t.Errorf("shift from %s", a.ShiftFrom)
	}
	for _, l := range a.Labels {
		if l.Proposed != "" || l.Reason != ReasonNoTextMatch {
			t.Errorf("%s: %q (%s), want none: text doesn't match", l.Label, l.Proposed, l.Reason)
		}
	}
}
