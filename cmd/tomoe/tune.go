package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// tuneCmd works out the best video-hint and diarization settings from a
// session recorded with record_for_tuning, against a reviewed transcript
// of the same meeting. Everything is replayed from what the session saved
// (its transcript, voice fingerprints and looks at the meeting window),
// so a whole sweep takes about a minute.
var tuneCmd = &cobra.Command{
	Use:   "tune <session-id>",
	Short: "Find the best speaker-naming and diarization settings from a recorded session",
	Long: "Replays a session recorded with record_for_tuning against a reviewed Teams transcript of the\n" +
		"same meeting: sweeps how names are attributed, how often the meeting window is looked at, the\n" +
		"fingerprint stride, and scores each by speaker accuracy, names\n" +
		"right and wrong, and CPU. Sparser looks and strides are simulated by thinning the recording.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString("ref")
		out, _ := cmd.Flags().GetString("out")
		offset, _ := cmd.Flags().GetFloat64("ref-offset")
		threads, _ := cmd.Flags().GetInt("threads")
		auto := !cmd.Flags().Changed("ref-offset")
		return runTune(args[0], ref, out, offset, auto, threads)
	},
}

func init() {
	tuneCmd.Flags().String("ref", "", "Teams transcript of the same meeting (a raw export works: Teams labels speakers from each person's own audio); without it, only what can be measured without an answer key")
	tuneCmd.Flags().String("out", "", "Output directory (default tune-<session>)")
	tuneCmd.Flags().Float64("ref-offset", 0, "Seconds to add to the reference's times to match the recording (default: estimated from the text)")
	tuneCmd.Flags().Int("threads", 2, "Threads per worker if fingerprints have to be computed")
	rootCmd.AddCommand(tuneCmd)
}

// tuneLook is a look reduced to what the sweep needs.
type tuneLook struct {
	t          float64 // session seconds
	name       string
	candidates []string
	costMs     float64
}

// tuneResult is one setting's scores.
type tuneResult struct {
	Stride       int                `json:"stride"`
	LookInterval float64            `json:"look_interval_seconds"` // 0: every recorded look
	Names        diarize.NameParams `json:"names"`

	SpeakerAcc float64 `json:"speaker_word_accuracy"`
	NameRight  float64 `json:"names_right"` // share of scored words labeled with the right name
	NameWrong  float64 `json:"names_wrong"` // ... a wrong name
	Unnamed    float64 `json:"unnamed"`     // ... no name
	Named      int     `json:"speakers_named"`
	HintCPU    float64 `json:"hint_cpu"` // share of one core the looks took
	Embeddings int     `json:"embeddings"`

	// Short turns: right speaker for words in reference turns of 1-3 and
	// 4-15 words. Small: speakers left with fewer than 20 words (lines
	// shown under a speaker who's barely there). MinVoiceSeconds and
	// MinWords are the small-speaker rules applied (0: off).
	Short13         float64 `json:"short_1_3"`
	Short415        float64 `json:"short_4_15"`
	Small           int     `json:"small_speakers"`
	MinVoiceSeconds float64 `json:"min_voice_seconds"`
	MinWords        int     `json:"min_words"`
}

// nameScore ranks settings: right names count, wrong names count double
// against (a wrong name is worse than none).
func (r tuneResult) nameScore() float64 { return r.NameRight - 2*r.NameWrong }

func runTune(id, refPath, outDir string, refOffset float64, autoOffset bool, threads int) error {
	began := time.Now()
	cfg, err := config.Load(config.Path())
	if err != nil {
		cfg = config.DefaultConfig()
	}
	status := models.NewManager(cfg.Transcription.ModelPath).Check()
	store := session.NewStore(config.SessionDir())
	sess, err := store.Load(id)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}
	dir := filepath.Join(config.SessionDir(), sess.ID)
	if outDir == "" {
		outDir = "tune-" + sess.ID[:min(8, len(sess.ID))]
	}

	// The session's transcribed words, in order.
	var segs []session.Segment
	for _, s := range sess.Segments {
		if len(s.Words) > 0 {
			segs = append(segs, s)
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].StartTime < segs[j].StartTime })
	var texts []string
	var times []float64
	for _, s := range segs {
		for _, w := range s.Words {
			texts = append(texts, w.Text)
			times = append(times, wordAt(w))
		}
	}
	if len(texts) == 0 {
		return fmt.Errorf("session %s has no word timings (recorded before they were saved?)", sess.ID)
	}

	// Fingerprints: saved by diarizing during the meeting, else computed.
	prep, info, err := loadFingerprints(dir)
	if err != nil {
		fmt.Printf("No saved fingerprints (%v); computing them from the recording...\n", err)
		sm, path, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, sess.Language)
		_, monitor, err := loadReplayTracks(sess)
		if err != nil {
			return err
		}
		workers := max(1, runtime.NumCPU()/max(1, threads))
		if prep, err = preparedDiarization(status.SpeakerSegmentationPath, path, monitor, workers, threads, nil); err != nil {
			return err
		}
		info = diarize.StreamInfo{Stride: 1, Params: diarize.DefaultParams()}
		info.Params.Threshold, info.Params.MergeSimilarity = sm.StreamThreshold, sm.StreamMerge
	}
	fmt.Printf("Fingerprints: %d, recorded every %d windows; clustering threshold %v, merge %v\n", len(prep.Embeddings), info.Stride, info.Params.Threshold, info.Params.MergeSimilarity)

	recorded, err := videohint.ReadLooks(dir)
	if err != nil {
		return err
	}
	var looks []tuneLook
	for _, l := range recorded {
		tl := tuneLook{t: l.Time.Sub(sess.CreatedAt).Seconds(), costMs: l.Cost.Capture + l.Cost.Detect + l.Cost.OCR + l.Cost.Encode}
		if name, candidates := l.Accepted(); name != "" {
			tl.name = name
		} else if candidates != nil {
			tl.candidates = candidates
		}
		looks = append(looks, tl)
	}
	fmt.Printf("Looks: %d (%d with a name)\n", len(looks), countNamed(looks))

	duration := sess.Duration
	if duration <= 0 {
		duration = times[len(times)-1]
	}
	if refPath == "" {
		writeHintReport(outDir, dir, sess, recorded, nil)
		return tuneWithoutRef(sess, segs, prep, info, recorded, looks, duration, cfg.Meeting.MinSpeakerWords, outDir, began)
	}

	f, err := os.Open(refPath)
	if err != nil {
		return err
	}
	ref, err := eval.ParseTeamsTranscript(f, sess.Duration+3600)
	f.Close()
	if err != nil {
		return fmt.Errorf("parsing reference: %w", err)
	}
	align := eval.NewWordAlignment(ref, texts)
	turnOf := make([]int, len(texts))
	for i := range texts {
		turnOf[i] = align.RefTurn(i)
	}
	if autoOffset {
		refOffset = estimateRefOffset(ref, turnOf, times)
	}
	ref = ref.Shift(refOffset)
	fmt.Printf("Reference: %d turns, %d speakers; shifted %+.1fs to match the recording\n", len(ref.Turns), len(ref.Speakers()), refOffset)
	writeHintReport(outDir, dir, sess, recorded, ref)

	sc := &tuneScorer{ref: ref, align: align, turnOf: turnOf, segs: segs, info: info, minWords: cfg.Meeting.MinSpeakerWords}

	strides := []int{info.Stride}
	for _, s := range []int{2, 3, 5} {
		if s > info.Stride && s%info.Stride == 0 {
			strides = append(strides, s)
		}
	}
	intervals := []float64{0, 0.7, 1, 2, 5}

	// Stage 1: naming rules, at each look rate, recorded stride.
	base := clusterStride(prep, info, info.Stride)
	var stage1 []tuneResult
	for _, iv := range intervals {
		hints, cpu := thinLooks(looks, iv, duration)
		for _, lag := range []float64{0, 0.25, 0.5, 1} {
			for _, minReads := range []int{1, 2, 3} {
				for _, share := range []float64{0.5, 0.6, 0.75} {
					for _, bucket := range []float64{2, 5, 10} {
						np := diarize.NameParams{Lag: lag, Bucket: bucket, MinReads: minReads, MinShare: share}
						r := sc.score(base, hints, np)
						r.Stride, r.LookInterval, r.HintCPU = info.Stride, iv, cpu
						stage1 = append(stage1, r)
					}
				}
			}
		}
	}
	sort.SliceStable(stage1, func(i, j int) bool { return stage1[i].nameScore() > stage1[j].nameScore() })
	best := stage1[0].Names

	// Stage 2: stride and look rate with the best rules.
	var stage2 []tuneResult
	for _, st := range strides {
		cs := clusterStride(prep, info, st)
		for _, iv := range intervals {
			hints, cpu := thinLooks(looks, iv, duration)
			r := sc.score(cs, hints, best)
			r.Stride, r.LookInterval, r.HintCPU = st, iv, cpu
			stage2 = append(stage2, r)
		}
	}
	def := diarize.DefaultNameParams()
	hints, cpu := thinLooks(looks, 0, duration)
	app := base
	app.clusters = base.prep.AbsorbSmallClusters(base.clusters, info.Params, cfg.Meeting.MinSpeakerSeconds)
	current := sc.score(app, hints, def)
	current.Stride, current.HintCPU = info.Stride, cpu

	// Stage 3: small speakers, absorbed by voice (into the most similar
	// larger cluster) and/or by neighbouring line, current naming rules.
	var stage3 []tuneResult
	for _, minWords := range []int{0, cfg.Meeting.MinSpeakerWords} {
		lines := *sc
		lines.minWords = minWords
		for _, secs := range []float64{0, 1, 2, 3, 5, 8, 12} {
			cs := base
			cs.clusters = base.prep.AbsorbSmallClusters(base.clusters, info.Params, secs)
			r := lines.score(cs, hints, def)
			r.Stride, r.HintCPU, r.MinVoiceSeconds = info.Stride, cpu, secs
			stage3 = append(stage3, r)
		}
		if minWords == 0 && cfg.Meeting.MinSpeakerWords == 0 {
			break
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	text := formatTune(sess, current, stage1, stage2) + formatSmallSpeakers(stage3)
	if err := os.WriteFile(filepath.Join(outDir, "tune.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(map[string]any{"current": current, "names": stage1, "stride_rate": stage2, "small_speakers": stage3}, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "tune.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (tune.txt, tune.json) in %s\n", outDir, formatDuration(time.Since(began).Seconds()))
	return nil
}

// estimateRefOffset is the median difference between when each reference
// turn's first aligned word was said in the recording and when the
// reference has the turn start.
func estimateRefOffset(ref *eval.Reference, turnOf []int, times []float64) float64 {
	seen := map[int]bool{}
	var diffs []float64
	for i, t := range turnOf {
		if t < 0 || seen[t] {
			continue
		}
		seen[t] = true
		diffs = append(diffs, times[i]-ref.Turns[t].Start)
	}
	if len(diffs) == 0 {
		return 0
	}
	sort.Float64s(diffs)
	return diffs[len(diffs)/2]
}

func loadFingerprints(dir string) (*diarize.Prepared, diarize.StreamInfo, error) {
	var info diarize.StreamInfo
	f, err := os.Open(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	var p diarize.Prepared
	if err := gob.NewDecoder(f).Decode(&p); err != nil {
		return nil, info, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "diarization.json"))
	if err != nil || json.Unmarshal(b, &info) != nil {
		info = diarize.StreamInfo{Stride: 1, Params: diarize.DefaultParams()}
	}
	if info.Stride < 1 {
		info.Stride = 1
	}
	return &p, info, nil
}

// clusterStride thins prep to every stride'th window and clusters it.
type strideClusters struct {
	prep     *diarize.Prepared
	clusters []int
}

func clusterStride(prep *diarize.Prepared, info diarize.StreamInfo, stride int) strideClusters {
	p := prep.EveryNth(stride)
	c := p.Cluster(info.Params.Threshold, 0)
	if info.Params.MergeSimilarity > 0 {
		c = p.MergeClusters(c, info.Params.MergeSimilarity)
	}
	return strideClusters{prep: p, clusters: c}
}

// thinLooks keeps the looks a watcher looking every interval seconds
// would have taken (0: all of them), as hints, and what they cost as a
// share of one core.
func thinLooks(looks []tuneLook, interval, duration float64) ([]diarize.Hint, float64) {
	var hints []diarize.Hint
	last := math.Inf(-1)
	cost := 0.0
	for _, l := range looks {
		if l.t-last < interval-0.01 {
			continue
		}
		last = l.t
		cost += l.costMs
		switch {
		case l.name != "":
			hints = append(hints, diarize.Hint{T: l.t, Name: l.name})
		case len(l.candidates) > 1:
			hints = append(hints, diarize.Hint{T: l.t, Candidates: l.candidates})
		}
	}
	return hints, cost / 1000 / duration
}

func countNamed(looks []tuneLook) int {
	n := 0
	for _, l := range looks {
		if l.name != "" {
			n++
		}
	}
	return n
}

// tuneScorer scores a clustering with hints against the reference.
type tuneScorer struct {
	ref    *eval.Reference
	align  *eval.WordAlignment
	turnOf []int
	segs   []session.Segment
	info   diarize.StreamInfo
	// minWords is MeetingConfig.MinSpeakerWords, applied as the app does.
	minWords int
}

func (s *tuneScorer) score(sc strideClusters, hints []diarize.Hint, np diarize.NameParams) tuneResult {
	turns := sc.prep.ReconstructClusters(sc.clusters, s.info.Params)
	for i := range turns {
		turns[i].Start += s.info.Offset
		turns[i].End += s.info.Offset
	}
	names := diarize.NameSpeakers(np, turns, hints, math.Inf(1))
	labels := map[int]string{}
	for _, t := range turns {
		if n := names[t.Speaker]; n != "" {
			labels[t.Speaker] = fmt.Sprintf("Person %d (%s)", t.Speaker+1, n)
		} else {
			labels[t.Speaker] = fmt.Sprintf("Person %d", t.Speaker+1)
		}
	}
	split, _ := session.SplitByDiarization(append([]session.Segment(nil), s.segs...), turns, labels)
	session.AbsorbSmallSpeakers(split, s.minWords, nil)
	words := segmentSpeakers(split)
	mapping := eval.ScoreSpeakers(s.ref, segmentsLabeled(split), 1.0).Mapping
	r := tuneResult{Names: np, Named: len(names), Embeddings: len(sc.prep.Embeddings), MinWords: s.minWords, Small: smallSpeakers(split, 20)}
	ws := s.align.Score(words, mapping)
	r.SpeakerAcc = ws.Accuracy()
	for _, b := range ws.Buckets {
		switch b.MaxWords {
		case 3:
			r.Short13 = b.Accuracy()
		case 15:
			r.Short415 = b.Accuracy()
		}
	}
	var right, wrong, none, total int
	for i, w := range words {
		t := s.turnOf[i]
		if t < 0 || len(w) == 0 {
			continue
		}
		total++
		switch name := session.HintName(w[0]); {
		case name == "":
			none++
		case diarize.SameName(name, s.ref.Turns[t].Speaker):
			right++
		default:
			wrong++
		}
	}
	if total > 0 {
		r.NameRight, r.NameWrong, r.Unnamed = float64(right)/float64(total), float64(wrong)/float64(total), float64(none)/float64(total)
	}
	return r
}

func formatTune(sess *session.Session, current tuneResult, names, rest []tuneResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tuning from session %q (%s).\n", sess.Title, formatDuration(sess.Duration))
	fmt.Fprintln(&b, "Scored by word against the reviewed transcript. speakers: right speaker (any name). names right/wrong/none:")
	fmt.Fprintln(&b, "share of words labeled with the right person's name, a wrong name, or none. Ranked by right minus twice wrong.")
	fmt.Fprintln(&b, "looks: every recorded look (0) or thinned to one every N seconds. hint CPU: share of one core the looks took")
	fmt.Fprintln(&b, "(approximate when thinned: fewer looks reuse known tiles less). Only final labels are scored here.")
	fmt.Fprintln(&b)
	row := func(r tuneResult) string {
		return fmt.Sprintf("  lag %4.2f  reads %d  share %.2f  bucket %4.1f | stride %d  looks %4.1f | speakers %s  names %s / %s / %s  named %2d | hint CPU %5.1f%%  fingerprints %d",
			r.Names.Lag, r.Names.MinReads, r.Names.MinShare, r.Names.Bucket, r.Stride, r.LookInterval,
			pct(r.SpeakerAcc), pct(r.NameRight), pct(r.NameWrong), pct(r.Unnamed), r.Named, 100*r.HintCPU, r.Embeddings)
	}
	fmt.Fprintln(&b, "Current defaults:")
	fmt.Fprintln(&b, row(current))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Naming rules (recorded stride), best 20:")
	for _, r := range names[:min(20, len(names))] {
		fmt.Fprintln(&b, row(r))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Stride and look rate, with the best naming rules:")
	for _, r := range rest {
		fmt.Fprintln(&b, row(r))
	}
	return b.String()
}

// formatSmallSpeakers reports stage 3 of tune: the small-speaker rules.
func formatSmallSpeakers(rs []tuneResult) string {
	var b strings.Builder
	fmt.Fprintln(&b, "\nSmall speakers (current naming rules, every look, recorded stride). voice: clusters speaking less than")
	fmt.Fprintln(&b, "N seconds in all join the most similar larger cluster; lines: speakers under N words take the neighbouring")
	fmt.Fprintln(&b, "line's speaker. small: speakers left with under 20 words. 1-3 / 4-15: right speaker in reference turns that long.")
	for _, r := range rs {
		fmt.Fprintf(&b, "  voice %4.1f s  lines %2d w | speakers %s  1-3 %s  4-15 %s | names %s / %s / %s | small %2d\n",
			r.MinVoiceSeconds, r.MinWords, pct(r.SpeakerAcc), pct(r.Short13), pct(r.Short415),
			pct(r.NameRight), pct(r.NameWrong), pct(r.Unnamed), r.Small)
	}
	return b.String()
}

// smallSpeakers counts the diarized speakers with fewer than minWords
// words.
func smallSpeakers(segs []session.Segment, minWords int) int {
	words := map[string]int{}
	for _, s := range segs {
		if session.Diarizable(s) {
			words[s.Speaker] += len(strings.Fields(s.Text))
		}
	}
	n := 0
	for _, w := range words {
		if w < minWords {
			n++
		}
	}
	return n
}
