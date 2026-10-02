package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// writeHintReport checks the video hints themselves, apart from what the
// clustering made of them: which windows were watched, when the watched
// window stopped repainting (its highlight then stays on whoever had it),
// and, with a reference, how often the name under the ring was the person
// the reference has speaking at that second. It prints the report and
// writes it to outDir/hints.txt. The report names people: it's for this
// computer only.
func writeHintReport(outDir, dir string, sess *session.Session, looks []videohint.Look, ref *eval.Reference) {
	text := hintReport(dir, sess, looks, ref)
	if err := os.MkdirAll(outDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, "hints.txt"), []byte(text), 0o644)
	}
	fmt.Println()
	fmt.Print(text)
}

func hintReport(dir string, sess *session.Session, looks []videohint.Look, ref *eval.Reference) string {
	var b strings.Builder
	at := func(t time.Time) float64 { return t.Sub(sess.CreatedAt).Seconds() }

	// Windows watched.
	type win struct {
		title string
		w, h  int
	}
	watched := map[win]int{}
	stages := map[string]int{}
	for _, l := range looks {
		stages[string(l.Stage)]++
		if l.Width > 0 {
			watched[win{l.Window, l.Width, l.Height}]++
		}
	}
	fmt.Fprintf(&b, "Video hints: %d looks.\n", len(looks))
	fmt.Fprintln(&b, "Windows watched (looks):")
	var ws []win
	for k := range watched {
		ws = append(ws, k)
	}
	sort.Slice(ws, func(i, j int) bool { return watched[ws[i]] > watched[ws[j]] })
	for _, k := range ws {
		fmt.Fprintf(&b, "  %6d  %4dx%-4d  %s\n", watched[k], k.w, k.h, k.title)
	}
	fmt.Fprintf(&b, "Stages: %s\n", countsLine(stages))

	// Stale stretches.
	stretches, _ := videohint.FrozenStretches(dir, looks)
	fmt.Fprintln(&b)
	if len(stretches) == 0 {
		fmt.Fprintln(&b, "Window not repainting: none found.")
	} else {
		total := 0.0
		for _, s := range stretches {
			total += s.End.Sub(s.Start).Seconds()
		}
		fmt.Fprintf(&b, "Window not repainting (speaker highlight stale): %d stretches, %.0f s in all.\n", len(stretches), total)
		for _, s := range stretches {
			names := map[string]int{}
			for _, l := range looks {
				if l.Usable && l.Name != "" && !l.Time.Before(s.Start) && !l.Time.After(s.End) {
					names[l.Name]++
				}
			}
			src := "looks marked ui_frozen"
			if s.FromFrames {
				src = "saved frames"
			}
			fmt.Fprintf(&b, "  %s  %s - %s  %4.0f s  (%s)  ring said: %s\n", clockOf(at(s.Start)), s.Start.Format("15:04:05"), s.End.Format("15:04:05"),
				s.End.Sub(s.Start).Seconds(), src, countsLine(names))
			if ref != nil {
				fmt.Fprintf(&b, "      reference speakers: %s\n", refSpeakersDuring(ref, at(s.Start), at(s.End)))
			}
		}
	}
	if ref == nil {
		return b.String()
	}

	// Ring against the reference, look by look.
	inStretch := func(t time.Time) bool {
		for _, s := range stretches {
			if !t.Before(s.Start) && !t.After(s.End) {
				return true
			}
		}
		return false
	}
	type tally struct {
		n, wrong int
		pairs    map[string]int
	}
	newTally := func() *tally { return &tally{pairs: map[string]int{}} }
	all, live, stale := newTally(), newTally(), newTally()
	perMinute := map[int]*tally{}
	for _, l := range looks {
		if !l.Usable || l.Name == "" {
			continue
		}
		t := at(l.Time)
		speakers := refSpeakersAt(ref, t)
		if len(speakers) == 0 {
			continue // outside the reference, or nobody speaking then
		}
		match := false
		for _, sp := range speakers {
			if diarize.SameName(l.Name, sp) {
				match = true
			}
		}
		m := int(t / 60)
		if perMinute[m] == nil {
			perMinute[m] = newTally()
		}
		group := live
		if inStretch(l.Time) {
			group = stale
		}
		for _, tl := range []*tally{all, group, perMinute[m]} {
			tl.n++
			if !match {
				tl.wrong++
				tl.pairs[fmt.Sprintf("ring %s, reference %s", firstName(l.Name), firstName(speakers[0]))]++
			}
		}
	}
	pct := func(t *tally) string {
		if t.n == 0 {
			return "no looks"
		}
		return fmt.Sprintf("%d of %d looks disagree (%.0f%%)", t.wrong, t.n, 100*float64(t.wrong)/float64(t.n))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Name under the ring vs the reference speaker at that second (looks with a name, inside the reference):")
	fmt.Fprintf(&b, "  all:                     %s\n", pct(all))
	fmt.Fprintf(&b, "  window repainting:       %s\n", pct(live))
	fmt.Fprintf(&b, "  window not repainting:   %s\n", pct(stale))
	fmt.Fprintln(&b, "  (a reference turn runs to the next one's start, so pauses and quick interjections")
	fmt.Fprintln(&b, "   blur the edges; the session's own mic speaker has no ring and reads as disagreement)")
	var mins []int
	for m := range perMinute {
		mins = append(mins, m)
	}
	sort.Slice(mins, func(i, j int) bool { return perMinute[mins[i]].wrong > perMinute[mins[j]].wrong })
	fmt.Fprintln(&b, "  worst minutes:")
	for _, m := range mins[:min(8, len(mins))] {
		t := perMinute[m]
		if t.wrong == 0 {
			break
		}
		fmt.Fprintf(&b, "    %s  %s; most: %s\n", clockOf(float64(m*60)), pct(t), topPair(t.pairs))
	}
	return b.String()
}

// refSpeakersAt is who the reference has speaking at t (session seconds).
func refSpeakersAt(ref *eval.Reference, t float64) []string {
	var out []string
	for _, tr := range ref.Turns {
		if t >= tr.Start && t < tr.End {
			out = append(out, tr.Speaker)
		}
	}
	return out
}

// refSpeakersDuring is each reference speaker's seconds in [from, to].
func refSpeakersDuring(ref *eval.Reference, from, to float64) string {
	secs := map[string]int{}
	for _, tr := range ref.Turns {
		if lo, hi := max(tr.Start, from), min(tr.End, to); hi > lo {
			secs[tr.Speaker] += int(hi - lo + 0.5)
		}
	}
	if len(secs) == 0 {
		return "none (outside the reference)"
	}
	return countsLine(secs) + " (seconds)"
}

// countsLine is "a 3, b 1" ordered by count.
func countsLine(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v || kvs[i].v == kvs[j].v && kvs[i].k < kvs[j].k })
	parts := make([]string, 0, len(kvs))
	for _, e := range kvs {
		parts = append(parts, fmt.Sprintf("%s %d", e.k, e.v))
	}
	return strings.Join(parts, ", ")
}

func topPair(m map[string]int) string {
	best, n := "", 0
	for k, v := range m {
		if v > n || v == n && k < best {
			best, n = k, v
		}
	}
	return fmt.Sprintf("%s (%d)", best, n)
}

func firstName(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// clockOf formats session seconds as h:mm:ss.
func clockOf(s float64) string {
	t := int(s + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", t/3600, t%3600/60, t%60)
}
