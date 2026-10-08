package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// noRefReport is what `tomoe tune` can measure without a reference
// transcript: what the hint layer saw and cost, how far the ring lags the
// voice, how consistent the names are, and how much a slower look rate or
// a higher stride changes the labels compared with recording-rate looks
// at stride 1.
type noRefReport struct {
	Looks       int                `json:"looks"`
	Stages      map[string]int     `json:"stages"`
	CostMs      map[string]float64 `json:"median_cost_ms"`
	CostP90Ms   float64            `json:"p90_look_ms"`
	CPU         float64            `json:"hint_cpu"`
	Names       map[string]int     `json:"names_read"`
	RingChanges int                `json:"ring_changes"`
	LagMatched  int                `json:"ring_changes_matched"`
	LagMedian   float64            `json:"ring_lag_median_seconds"`
	LagP25      float64            `json:"ring_lag_p25_seconds"`
	LagP75      float64            `json:"ring_lag_p75_seconds"`
	Speakers    []noRefSpeaker     `json:"speakers"`
	Variants    []noRefVariant     `json:"variants"`
}

type noRefSpeaker struct {
	Label     string  `json:"label"`
	Seconds   float64 `json:"speech_seconds"`
	Name      string  `json:"name"`
	Reads     int     `json:"reads"`
	Agreeing  float64 `json:"reads_agreeing"`
	TopOthers string  `json:"other_names"`
}

type noRefVariant struct {
	Stride       int     `json:"stride"`
	LookInterval float64 `json:"look_interval_seconds"`
	Lag          float64 `json:"lag_seconds"`
	Agreement    float64 `json:"same_name_as_full"` // share of words with the same name as full rate, stride 1
	Named        float64 `json:"words_named"`
	CPU          float64 `json:"hint_cpu"`
	Embeddings   int     `json:"embeddings"`
}

func tuneWithoutRef(sess *session.Session, segs []session.Segment, prep *diarize.Prepared, info diarize.StreamInfo, recorded []videohint.Look, looks []tuneLook, duration float64, minWords int, outDir string, began time.Time) error {
	r := noRefReport{Looks: len(recorded), Stages: map[string]int{}, CostMs: map[string]float64{}, Names: map[string]int{}}
	var capture, detect, ocr, encode, total []float64
	for _, l := range recorded {
		r.Stages[string(l.Stage)]++
		capture = append(capture, l.Cost.Capture)
		detect = append(detect, l.Cost.Detect)
		if l.Cost.OCR > 0 {
			ocr = append(ocr, l.Cost.OCR)
		}
		encode = append(encode, l.Cost.Encode)
		total = append(total, l.Cost.Capture+l.Cost.Detect+l.Cost.OCR+l.Cost.Encode)
		if l.Usable && l.Name != "" {
			r.Names[l.Name]++
		}
	}
	r.CostMs["capture"], r.CostMs["rings"], r.CostMs["name read (when run)"], r.CostMs["encode"], r.CostMs["look"] =
		median(capture), median(detect), median(ocr), median(encode), median(total)
	r.CostP90Ms = quantile(total, 0.9)
	_, r.CPU = thinLooks(looks, 0, duration)

	// The full-rate timeline, stride as recorded, and its names.
	full := clusterStride(prep, info, info.Stride)
	turns := sessionTurns(full, info)
	hints, _ := thinLooks(looks, 0, duration)
	def := diarize.DefaultNameParams()
	names := diarize.NameSpeakers(def, turns, hints, math.Inf(1))

	// Ring lag: when the name under the ring changes, how long after the
	// timeline's speaker change nearest before it.
	var ringChanges []float64
	prev := ""
	for _, l := range looks {
		if l.name == "" {
			continue
		}
		if prev != "" && !diarize.SameName(prev, l.name) {
			ringChanges = append(ringChanges, l.t)
		}
		prev = l.name
	}
	changes := speakerChanges(turns)
	var lags []float64
	for _, rc := range ringChanges {
		best := math.Inf(1)
		for _, c := range changes {
			if d := rc - c; d >= -1 && d <= 4 && math.Abs(d) < math.Abs(best) {
				best = d
			}
		}
		if !math.IsInf(best, 1) {
			lags = append(lags, best)
		}
	}
	r.RingChanges, r.LagMatched = len(ringChanges), len(lags)
	r.LagMedian, r.LagP25, r.LagP75 = median(lags), quantile(lags, 0.25), quantile(lags, 0.75)

	// Per speaker: speech, name, and how its reads agree.
	speech := map[int]float64{}
	for _, t := range turns {
		speech[t.Speaker] += t.End - t.Start
	}
	reads := map[int]map[string]int{}
	for _, h := range hints {
		if h.Name == "" {
			continue
		}
		if a := activeSpeakers(turns, h.T-def.Lag); len(a) == 1 {
			if reads[a[0]] == nil {
				reads[a[0]] = map[string]int{}
			}
			reads[a[0]][h.Name]++
		}
	}
	for spk, secs := range speech {
		if secs < 10 {
			continue
		}
		sp := noRefSpeaker{Label: fmt.Sprintf("Person %d", spk+1), Seconds: secs, Name: names[spk]}
		var others []string
		agree := 0
		for n, k := range reads[spk] {
			sp.Reads += k
			if sp.Name != "" && diarize.SameName(n, sp.Name) {
				agree += k
			} else {
				others = append(others, fmt.Sprintf("%s ×%d", n, k))
			}
		}
		if sp.Reads > 0 {
			sp.Agreeing = float64(agree) / float64(sp.Reads)
		}
		sort.Strings(others)
		if len(others) > 4 {
			others = append(others[:4], "…")
		}
		sp.TopOthers = strings.Join(others, ", ")
		r.Speakers = append(r.Speakers, sp)
	}
	sort.Slice(r.Speakers, func(i, j int) bool { return r.Speakers[i].Seconds > r.Speakers[j].Seconds })

	// Variants against the full-rate labels.
	base := wordNames(segs, turns, names, minWords)
	// Only the other side's words: the mic is always "You".
	var otherSide []bool
	for _, s := range segs {
		for range s.Words {
			otherSide = append(otherSide, session.Diarizable(s))
		}
	}
	strides := []int{info.Stride}
	for _, s := range []int{2, 3, 5} {
		if s > info.Stride && s%info.Stride == 0 {
			strides = append(strides, s)
		}
	}
	for _, st := range strides {
		sc := full
		if st != info.Stride {
			sc = clusterStride(prep, info, st)
		}
		ts := sessionTurns(sc, info)
		for _, iv := range []float64{0, 0.7, 1, 2, 5} {
			h, cpu := thinLooks(looks, iv, duration)
			for _, lag := range []float64{0, 0.5, 1} {
				np := def
				np.Lag = lag
				got := wordNames(segs, ts, diarize.NameSpeakers(np, ts, h, math.Inf(1)), minWords)
				same, named, other := 0, 0, 0
				for i := range got {
					if !otherSide[i] {
						continue
					}
					other++
					if got[i] == base[i] {
						same++
					}
					if got[i] != "" {
						named++
					}
				}
				r.Variants = append(r.Variants, noRefVariant{
					Stride: st, LookInterval: iv, Lag: lag, CPU: cpu, Embeddings: len(sc.prep.Embeddings),
					Agreement: float64(same) / float64(max(1, other)), Named: float64(named) / float64(max(1, other)),
				})
			}
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	text := formatNoRef(sess, r)
	if err := os.WriteFile(filepath.Join(outDir, "tune-noref.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "tune-noref.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (tune-noref.txt, tune-noref.json) in %s\n", outDir, formatDuration(time.Since(began).Seconds()))
	return nil
}

// sessionTurns reconstructs a clustering's turns in session time.
func sessionTurns(sc strideClusters, info diarize.StreamInfo) []session.DiarizeSegment {
	turns := sc.prep.ReconstructClusters(sc.clusters, info.Params)
	for i := range turns {
		turns[i].Start += info.Offset
		turns[i].End += info.Offset
	}
	return turns
}

// speakerChanges are the times the timeline's speaker changes.
func speakerChanges(turns []session.DiarizeSegment) []float64 {
	ts := append([]session.DiarizeSegment(nil), turns...)
	sort.Slice(ts, func(i, j int) bool { return ts[i].Start < ts[j].Start })
	var out []float64
	for i := 1; i < len(ts); i++ {
		if ts[i].Speaker != ts[i-1].Speaker {
			out = append(out, ts[i].Start)
		}
	}
	return out
}

func activeSpeakers(turns []session.DiarizeSegment, t float64) []int {
	var out []int
	for _, tr := range turns {
		if tr.Start <= t && t < tr.End {
			out = append(out, tr.Speaker)
		}
	}
	return out
}

// wordNames labels every transcribed word with its speaker's name ("" if
// unnamed), as the app would split and label lines.
func wordNames(segs []session.Segment, turns []session.DiarizeSegment, names map[int]string, minWords int) []string {
	labels := map[int]string{}
	for _, t := range turns {
		if n := names[t.Speaker]; n != "" {
			labels[t.Speaker] = fmt.Sprintf("Person %d (%s)", t.Speaker+1, n)
		} else {
			labels[t.Speaker] = fmt.Sprintf("Person %d", t.Speaker+1)
		}
	}
	split, _ := session.SplitByDiarization(append([]session.Segment(nil), segs...), turns, labels)
	session.AbsorbSmallSpeakers(split, minWords, nil)
	var out []string
	for _, w := range segmentSpeakers(split) {
		n := ""
		if len(w) > 0 {
			n = session.HintName(w[0])
		}
		out = append(out, n)
	}
	return out
}

func median(v []float64) float64 { return quantile(v, 0.5) }

func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

func formatNoRef(sess *session.Session, r noRefReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hint analysis of %q (%s) without a reference transcript.\n\n", sess.Title, formatDuration(sess.Duration))
	stages := make([]string, 0, len(r.Stages))
	for s := range r.Stages {
		stages = append(stages, s)
	}
	sort.Slice(stages, func(i, j int) bool { return r.Stages[stages[i]] > r.Stages[stages[j]] })
	fmt.Fprintf(&b, "Looks: %d.", r.Looks)
	for _, s := range stages {
		fmt.Fprintf(&b, " %s %d (%.0f%%)", s, r.Stages[s], 100*float64(r.Stages[s])/float64(max(1, r.Looks)))
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Cost per look, median: capture %.1f ms, rings %.1f ms, name read %.1f ms (when run), encode %.1f ms; look %.1f ms, 90th %.1f ms.\n",
		r.CostMs["capture"], r.CostMs["rings"], r.CostMs["name read (when run)"], r.CostMs["encode"], r.CostMs["look"], r.CostP90Ms)
	fmt.Fprintf(&b, "CPU at the recorded rate: %.1f%% of one core.\n\n", 100*r.CPU)

	names := make([]string, 0, len(r.Names))
	for n := range r.Names {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return r.Names[names[i]] > r.Names[names[j]] })
	fmt.Fprint(&b, "Names read:")
	for _, n := range names {
		fmt.Fprintf(&b, " %s ×%d;", n, r.Names[n])
	}
	fmt.Fprintln(&b)
	if r.RingChanges == 0 {
		fmt.Fprintln(&b, "Ring lag: not measurable, the ring never moved between people (one remote speaker?).")
		fmt.Fprintln(&b)
	} else {
		fmt.Fprintf(&b, "Ring lag behind the voice: median %.2f s (middle half %.2f to %.2f s), from %d of %d ring moves matched to a timeline speaker change.\n\n",
			r.LagMedian, r.LagP25, r.LagP75, r.LagMatched, r.RingChanges)
	}

	fmt.Fprintln(&b, "Timeline speakers with 10 s or more of speech (default rules):")
	for _, sp := range r.Speakers {
		name := sp.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&b, "  %-10s %6.0fs  %-28s reads %4d, agreeing %s   other reads: %s\n", sp.Label, sp.Seconds, name, sp.Reads, pct(sp.Agreeing), sp.TopOthers)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Variants vs looks at the recorded rate, stride as recorded. Of the other side's words (not the mic's): same name as")
	fmt.Fprintln(&b, "at the recorded rate, and named at all:")
	for _, v := range r.Variants {
		fmt.Fprintf(&b, "  stride %d  looks %4.1f s  lag %.1f s | same name %s  words named %s | hint CPU %5.1f%%  fingerprints %d\n",
			v.Stride, v.LookInterval, v.Lag, pct(v.Agreement), pct(v.Named), 100*v.CPU, v.Embeddings)
	}
	return b.String()
}
