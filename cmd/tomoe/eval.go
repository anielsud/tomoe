package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
)

// evalCmd scores every pass of the pipeline against a reference transcript.
var evalCmd = &cobra.Command{
	Use:   "eval <audio-or-video> --ref <transcript.txt>",
	Short: "Score each transcription and speaker pass against a reference transcript",
	Long: `Runs a recording through the pipeline and scores each pass separately
against a reference transcript (a Microsoft Teams transcript export):

  Speech detection  how much of the reference the pipeline heard at all
  Text              pass 1 (streaming, two-pass runs only) and final (Parakeet)
                    word error rate
  Speakers          live guess, initial diarization, refined diarization
                    (after merging similar clusters) and final transcript
                    labels: confusion, purity, coverage, speaker count
  Who said what     word errors counting a right word with the wrong
                    speaker as wrong
  Quick exchanges   short turns squeezed between other speakers (the
                    closest proxy for cross-talk a Teams export supports)

The whole recording is treated as one mixed track, so every voice
(including the host) is clustered. A Teams transcript is itself automatic,
so text scores measure disagreement with Teams, not true accuracy.

Writes report.txt, scores.json and per-pass transcripts for spot checks to
--out.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := evalOptions{media: args[0]}
		opts.ref, _ = cmd.Flags().GetString("ref")
		opts.out, _ = cmd.Flags().GetString("out")
		opts.collar, _ = cmd.Flags().GetFloat64("collar")
		opts.skipDiarization, _ = cmd.Flags().GetBool("skip-diarization")
		opts.runs, _ = cmd.Flags().GetStringSlice("runs")
		opts.threads, _ = cmd.Flags().GetInt("threads")
		opts.noCache, _ = cmd.Flags().GetBool("no-cache")
		var err error
		for name, dst := range map[string]*float64{"from": &opts.from, "to": &opts.to} {
			v, _ := cmd.Flags().GetString(name)
			if *dst, err = parseSeconds(v); err != nil {
				return fmt.Errorf("--%s: %w", name, err)
			}
		}
		return runEval(opts)
	},
}

func init() {
	evalCmd.Flags().String("ref", "", "Reference transcript (Teams transcript export as .txt)")
	evalCmd.Flags().String("out", "", "Output directory (default: eval-<media name> in the current directory)")
	evalCmd.Flags().Float64("collar", 1.0, "Seconds around each reference speaker change not scored (Teams timestamps are to the second)")
	evalCmd.Flags().Bool("skip-diarization", false, "Skip the post-meeting diarization passes (much faster)")
	evalCmd.Flags().StringSlice("runs", []string{"default", "experimental"}, "Pipeline settings to run: default, experimental")
	evalCmd.Flags().String("from", "", "Score only from this point (e.g. 10m, 90s, 1h5m)")
	evalCmd.Flags().String("to", "", "Score only up to this point (e.g. 20m)")
	evalCmd.Flags().Int("threads", 0, "CPU threads per parallel job (default: all cores split between jobs)")
	evalCmd.Flags().Bool("no-cache", false, "Recompute transcription and diarization instead of reusing earlier results")
	_ = evalCmd.MarkFlagRequired("ref")
	rootCmd.AddCommand(evalCmd)
}

type evalOptions struct {
	media, ref, out string
	collar          float64
	skipDiarization bool
	runs            []string
	from, to        float64 // seconds; to 0 = the end
	threads         int
	noCache         bool
}

// evalRun is one pipeline configuration's results.
type evalRun struct {
	Name    string `json:"name"`
	TwoPass bool   `json:"two_pass"`
	Tuning  string `json:"tuning"`

	Detection eval.DetectionScore `json:"speech_detection"`

	TextPass1 *eval.ErrorCounts `json:"text_pass1,omitempty"`
	TextFinal eval.ErrorCounts  `json:"text_final"`

	SpeakersLive  eval.SpeakerScore  `json:"speakers_live"`
	SpeakersFinal *eval.SpeakerScore `json:"speakers_final,omitempty"`

	WhoSaidWhatLive  eval.ErrorCounts  `json:"who_said_what_live"`
	WhoSaidWhatFinal *eval.ErrorCounts `json:"who_said_what_final,omitempty"`

	ExchangesLive  eval.ExchangeScore  `json:"quick_exchanges_live"`
	ExchangesFinal *eval.ExchangeScore `json:"quick_exchanges_final,omitempty"`

	OverlapsLive  eval.OverlapScore  `json:"annotated_overlaps_live"`
	OverlapsFinal *eval.OverlapScore `json:"annotated_overlaps_final,omitempty"`

	// The final pass with lines split where diarization changes speaker
	// mid-line (split_on_speaker_change).
	SpeakersSplit    *eval.SpeakerScore  `json:"speakers_final_split,omitempty"`
	WhoSaidWhatSplit *eval.ErrorCounts   `json:"who_said_what_final_split,omitempty"`
	ExchangesSplit   *eval.ExchangeScore `json:"quick_exchanges_final_split,omitempty"`
	OverlapsSplit    *eval.OverlapScore  `json:"annotated_overlaps_final_split,omitempty"`

	Seconds float64 `json:"run_seconds"`

	segs               []session.Segment // the run's final segments, with word timings
	live, final, split []eval.Labeled
	pass1              []eval.Labeled
}

// evalReport is scores.json.
type evalReport struct {
	Media       string  `json:"media"`
	Reference   string  `json:"reference"`
	AudioSecs   float64 `json:"audio_seconds"`
	RefTurns    int     `json:"ref_turns"`
	RefSpeakers int     `json:"ref_speakers"`
	RefWords    int     `json:"ref_words"`
	Collar      float64 `json:"collar_seconds"`
	From        float64 `json:"from_seconds"`
	To          float64 `json:"to_seconds"`
	WallSecs    float64 `json:"wall_seconds"`
	CacheHits   int64   `json:"transcription_cache_hits"`
	CacheMisses int64   `json:"transcription_cache_misses"`

	DiarizationInitial *eval.SpeakerScore `json:"diarization_initial,omitempty"`
	DiarizationRefined *eval.SpeakerScore `json:"diarization_refined,omitempty"`
	OverlapsInitial    *eval.OverlapScore `json:"annotated_overlaps_diarization_initial,omitempty"`
	OverlapsRefined    *eval.OverlapScore `json:"annotated_overlaps_diarization_refined,omitempty"`
	RefWarnings        []string           `json:"reference_warnings,omitempty"`
	DiarizationSecs    float64            `json:"diarization_seconds,omitempty"`

	Runs []*evalRun `json:"runs"`
}

func runEval(opts evalOptions) error {
	began := time.Now()
	cfg, err := config.Load(config.Path())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	status := models.NewManager(cfg.Transcription.ModelPath).Check()
	if !status.Ready() {
		return fmt.Errorf("transcription models not downloaded (run 'tomoe model download')")
	}
	if !opts.skipDiarization && !status.DiarizationReady() {
		return fmt.Errorf("diarization models not downloaded (run 'tomoe model download', or pass --skip-diarization)")
	}

	fmt.Printf("Decoding %s...\n", filepath.Base(opts.media))
	samples, err := session.DecodeToFloat32(opts.media)
	if err != nil {
		return fmt.Errorf("decoding audio: %w", err)
	}
	fullSecs := float64(len(samples)) / 16000

	f, err := os.Open(opts.ref)
	if err != nil {
		return err
	}
	ref, err := eval.ParseTeamsTranscript(f, fullSecs)
	f.Close()
	if err != nil {
		return fmt.Errorf("parsing reference: %w", err)
	}
	for _, w := range ref.Warnings {
		fmt.Printf("Reference warning: %s\n", w)
	}

	from, to := opts.from, opts.to
	if to <= 0 || to > fullSecs {
		to = fullSecs
	}
	if from < 0 || from >= to {
		return fmt.Errorf("--from must be before --to and inside the recording (%s)", formatDuration(fullSecs))
	}
	if from > 0 || to < fullSecs {
		samples = samples[int(from*16000):int(to*16000)]
		ref = ref.Slice(from, to)
		fmt.Printf("Scoring %s to %s only\n", formatDuration(from), formatDuration(to))
	}
	audioSecs := float64(len(samples)) / 16000
	var refWords []string
	for _, t := range ref.Turns {
		refWords = append(refWords, eval.Words(t.Text)...)
	}

	report := &evalReport{
		Media: filepath.Base(opts.media), Reference: filepath.Base(opts.ref), AudioSecs: audioSecs,
		From: from, To: to,
		RefTurns: len(ref.Turns), RefSpeakers: len(ref.Speakers()), RefWords: len(refWords), Collar: opts.collar,
		RefWarnings: ref.Warnings,
	}
	fmt.Printf("Reference: %d turns, %d speakers, %d words over %s\n", report.RefTurns, report.RefSpeakers, report.RefWords, formatDuration(audioSecs))

	runs, err := evalRuns(opts.runs)
	if err != nil {
		return err
	}

	var cache *evalCache
	if !opts.noCache {
		textKey := fmt.Sprintf("%s|%s|%s|%d|%s|%v|%v", status.EncoderPath, status.DecoderPath, cfg.Transcription.DecodingMethod,
			cfg.Transcription.MaxActivePaths, cfg.Transcription.HotwordsFile, cfg.Transcription.HotwordsScore, cfg.Transcription.GPUEnabled)
		if cache, err = openEvalCache(samples, textKey); err != nil {
			fmt.Printf("Note: running without a cache: %v\n", err)
			cache = nil
		}
	}

	// Every run and the diarization pass are independent, so they run at
	// once, splitting the CPU between them.
	jobs := len(runs)
	if !opts.skipDiarization {
		jobs++
	}
	threads := opts.threads
	if threads <= 0 {
		threads = max(2, runtime.NumCPU()/max(1, jobs))
	}
	fmt.Printf("Running %d jobs in parallel, %d threads each\n", jobs, threads)

	var wg sync.WaitGroup
	errs := make(chan error, jobs)

	var diar *cachedDiarization
	if !opts.skipDiarization {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, secs, err := runDiarization(cfg, status, samples, threads, cache)
			if err != nil {
				errs <- fmt.Errorf("diarization: %w", err)
				return
			}
			diar, report.DiarizationSecs = d, secs
		}()
	}
	for _, run := range runs {
		wg.Add(1)
		go func(run *evalRun) {
			defer wg.Done()
			if err := runPipeline(run, cfg, status, samples, threads, cache); err != nil {
				errs <- fmt.Errorf("%s run: %w", run.Name, err)
			}
		}(run)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}
	if cache != nil {
		if err := cache.save(); err != nil {
			fmt.Printf("Note: couldn't save the transcription cache: %v\n", err)
		}
		report.CacheHits, report.CacheMisses = cache.hits.Load(), cache.misses.Load()
	}

	for _, run := range runs {
		scoreRun(run, ref, refWords, opts.collar)
	}
	var initial, refined []eval.Labeled
	if diar != nil {
		initial = diarLabeled(diar.Raw, diar.RawMap)
		refined = diarLabeled(diar.Merged, diar.MergedMap)
		si := eval.ScoreSpeakers(ref, initial, opts.collar)
		sr := eval.ScoreSpeakers(ref, refined, opts.collar)
		report.DiarizationInitial, report.DiarizationRefined = &si, &sr
		oi := eval.ScoreAnnotatedOverlaps(ref, initial, si.Mapping, 3)
		or := eval.ScoreAnnotatedOverlaps(ref, refined, sr.Mapping, 3)
		report.OverlapsInitial, report.OverlapsRefined = &oi, &or
		for _, run := range runs {
			segs := append([]session.Segment(nil), run.segs...)
			session.RelabelByDiarization(segs, diar.Merged, diar.MergedMap)
			run.final = segmentsLabeled(segs)
			split, _ := session.SplitByDiarization(append([]session.Segment(nil), run.segs...), diar.Merged, diar.MergedMap)
			run.split = segmentsLabeled(split)
			ss := eval.ScoreSpeakers(ref, run.split, opts.collar)
			ws := eval.SpeakerAttributedErrors(ref, run.split, ss.Mapping)
			xs := eval.ScoreQuickExchanges(ref, run.split, ss.Mapping, 8, 6, 3)
			ovs := eval.ScoreAnnotatedOverlaps(ref, run.split, ss.Mapping, 3)
			run.SpeakersSplit, run.WhoSaidWhatSplit, run.ExchangesSplit, run.OverlapsSplit = &ss, &ws, &xs, &ovs
			sf := eval.ScoreSpeakers(ref, run.final, opts.collar)
			wf := eval.SpeakerAttributedErrors(ref, run.final, sf.Mapping)
			xf := eval.ScoreQuickExchanges(ref, run.final, sf.Mapping, 8, 6, 3)
			of := eval.ScoreAnnotatedOverlaps(ref, run.final, sf.Mapping, 3)
			run.SpeakersFinal, run.WhoSaidWhatFinal, run.ExchangesFinal, run.OverlapsFinal = &sf, &wf, &xf, &of
		}
	}
	report.Runs = runs
	report.WallSecs = time.Since(began).Seconds()

	outDir := opts.out
	if outDir == "" {
		outDir = "eval-" + strings.TrimSuffix(filepath.Base(opts.media), filepath.Ext(opts.media))
	}
	if err := writeEvalOutputs(outDir, report, ref, initial, refined); err != nil {
		return err
	}
	text := formatEvalReport(report)
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (report.txt, scores.json, per-pass transcripts, spotcheck.txt)\n", outDir)
	return nil
}

// runPipeline runs one configuration through the live pipeline with its own
// models, serving transcription from cache where it can.
func runPipeline(run *evalRun, cfg *config.Config, status *models.Status, samples []float32, threads int, cache *evalCache) error {
	pipe, err := loadOfflinePipelineThreads(cfg, status, "en", true, run.TwoPass, threads)
	if err != nil {
		return err
	}
	defer pipe.Close()
	if run.TwoPass && pipe.streaming == nil {
		run.TwoPass = false
	}
	if cache != nil {
		pipe.engine = cache.wrap(pipe.engine)
	}
	fmt.Printf("Running %s pipeline (%s)...\n", run.Name, run.Tuning)
	began := time.Now()
	res, err := live.ReplayDetailed(pipe.liveConfig(tuningFor(run.Name), run.TwoPass), nil, samples)
	if err != nil {
		return err
	}
	run.Seconds = time.Since(began).Seconds()
	run.segs = res.Segments
	sort.SliceStable(run.segs, func(i, j int) bool { return run.segs[i].StartTime < run.segs[j].StartTime })
	for _, s := range res.Segments {
		run.live = append(run.live, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: s.Text})
		if p1, ok := res.Pass1Text[s.ID]; ok {
			run.pass1 = append(run.pass1, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: p1})
		}
	}
	sortLabeled(run.live)
	sortLabeled(run.pass1)
	fmt.Printf("  %s pipeline done in %s\n", run.Name, formatDuration(run.Seconds))
	return nil
}

// runDiarization runs the post-meeting diarization pass (initial, then
// merged), or loads it from the cache.
func runDiarization(cfg *config.Config, status *models.Status, samples []float32, threads int, cache *evalCache) (*cachedDiarization, float64, error) {
	const threshold, merge = 1.1, 0.55 // the post-save diarization's settings (cmd/tomoe/diarize.go)
	key := fmt.Sprintf("%s|%s|%v|%v", status.SpeakerSegmentationPath, status.SpeakerEmbeddingPath, threshold, merge)
	if cache != nil {
		if d, ok := cache.loadDiarization(key); ok {
			fmt.Println("Diarization loaded from cache")
			return d, 0, nil
		}
	}
	fmt.Println("Running post-meeting diarization...")
	began := time.Now()
	raw, rawMap, err := session.Diarize(samples, session.DiarizeConfig{
		SegmentationModelPath: status.SpeakerSegmentationPath,
		EmbeddingModelPath:    status.SpeakerEmbeddingPath,
		Threshold:             threshold,
		UseGPU:                cfg.Transcription.GPUEnabled,
		NumThreads:            threads,
	})
	if err != nil {
		return nil, 0, err
	}
	merged, mergedMap := session.MergeSimilarSpeakers(append([]session.DiarizeSegment(nil), raw...), copyMap(rawMap), samples, status.SpeakerEmbeddingPath, merge, false)
	d := &cachedDiarization{Raw: raw, RawMap: rawMap, Merged: merged, MergedMap: mergedMap}
	secs := time.Since(began).Seconds()
	fmt.Printf("  diarization done in %s\n", formatDuration(secs))
	if cache != nil {
		if err := cache.storeDiarization(key, d); err != nil {
			fmt.Printf("Note: couldn't cache diarization: %v\n", err)
		}
	}
	return d, secs, nil
}

// evalRuns turns --runs names into runs.
func evalRuns(names []string) ([]*evalRun, error) {
	var runs []*evalRun
	for _, n := range names {
		switch n {
		case "default":
			runs = append(runs, &evalRun{Name: n, Tuning: "single-pass; threshold 0.65, sticky and short-segment rules off"})
		case "experimental":
			runs = append(runs, &evalRun{Name: n, TwoPass: true, Tuning: "two-pass; threshold 0.55, sticky 0.15, short-segment 0.7s"})
		default:
			return nil, fmt.Errorf("unknown run %q (want default or experimental)", n)
		}
	}
	return runs, nil
}

func tuningFor(run string) speaker.Tuning {
	if run == "experimental" {
		return speaker.ExperimentalTuning()
	}
	return speaker.DefaultTuning()
}

func scoreRun(run *evalRun, ref *eval.Reference, refWords []string, collar float64) {
	run.Detection = eval.ScoreDetection(ref, run.live, 0.5)
	run.TextFinal = eval.Align(refWords, labeledWords(run.live))
	if run.TwoPass {
		p1 := eval.Align(refWords, labeledWords(run.pass1))
		run.TextPass1 = &p1
	}
	run.SpeakersLive = eval.ScoreSpeakers(ref, run.live, collar)
	run.WhoSaidWhatLive = eval.SpeakerAttributedErrors(ref, run.live, run.SpeakersLive.Mapping)
	run.ExchangesLive = eval.ScoreQuickExchanges(ref, run.live, run.SpeakersLive.Mapping, 8, 6, 3)
	run.OverlapsLive = eval.ScoreAnnotatedOverlaps(ref, run.live, run.SpeakersLive.Mapping, 3)
}

func segmentsLabeled(segs []session.Segment) []eval.Labeled {
	out := make([]eval.Labeled, 0, len(segs))
	for _, s := range segs {
		out = append(out, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: s.Text})
	}
	sortLabeled(out)
	return out
}

func labeledWords(ls []eval.Labeled) []string {
	var w []string
	for _, l := range ls {
		w = append(w, eval.Words(l.Text)...)
	}
	return w
}

func sortLabeled(ls []eval.Labeled) {
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Start < ls[j].Start })
}

func diarLabeled(segs []session.DiarizeSegment, labels map[int]string) []eval.Labeled {
	out := make([]eval.Labeled, 0, len(segs))
	for _, s := range segs {
		out = append(out, eval.Labeled{Start: s.Start, End: s.End, Speaker: labels[s.Speaker]})
	}
	sortLabeled(out)
	return out
}

func copyMap(m map[int]string) map[int]string {
	out := make(map[int]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func pct(x float64) string { return fmt.Sprintf("%5.1f%%", 100*x) }

func speakerLine(name string, s *eval.SpeakerScore) string {
	if s == nil {
		return fmt.Sprintf("  %-26s %s\n", name, "(skipped)")
	}
	return fmt.Sprintf("  %-26s %s  %s  %s  %3d vs %d   %6.0fs\n", name, pct(s.Confusion), pct(s.Purity), pct(s.Coverage), s.HypSpeakers, s.RefSpeakers, s.OverlapSeconds)
}

// formatEvalReport renders the human-readable report.
func formatEvalReport(r *evalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Eval: %s against %s\n", r.Media, r.Reference)
	fmt.Fprintf(&b, "Reference: %d turns, %d speakers, %d words, %s. Speaker scores exclude %.1fs around each speaker change.\n",
		r.RefTurns, r.RefSpeakers, r.RefWords, formatDuration(r.AudioSecs), r.Collar)
	fmt.Fprintln(&b, "Text scores are against the reference transcript: disagreement with Teams unless it was reviewed by hand.")
	for _, w := range r.RefWarnings {
		fmt.Fprintf(&b, "Reference warning: %s\n", w)
	}
	for _, run := range r.Runs {
		fmt.Fprintf(&b, "\n== %s run: %s ==\n", run.Name, run.Tuning)
		d := run.Detection
		fmt.Fprintf(&b, "Speech detection   heard %d/%d turns (%s), %d/%d words (%s)\n",
			d.TurnsHeard, d.Turns, pct(ratio(d.TurnsHeard, d.Turns)), d.WordsHeard, d.Words, pct(ratio(d.WordsHeard, d.Words)))
		fmt.Fprintln(&b, "Text               word error  (subs / missing / extra)")
		if run.TextPass1 != nil {
			fmt.Fprintf(&b, "  %-16s %s     (%d / %d / %d)\n", "pass 1 (live)", pct(run.TextPass1.Rate()), run.TextPass1.Substitutions, run.TextPass1.Deletions, run.TextPass1.Insertions)
		}
		fmt.Fprintf(&b, "  %-16s %s     (%d / %d / %d)\n", "final", pct(run.TextFinal.Rate()), run.TextFinal.Substitutions, run.TextFinal.Deletions, run.TextFinal.Insertions)
		fmt.Fprintln(&b, "Speakers                     confusion purity  coverage speakers  overlap")
		b.WriteString(speakerLine("live guess", &run.SpeakersLive))
		if r.DiarizationInitial != nil {
			b.WriteString(speakerLine("initial diarization", r.DiarizationInitial))
			b.WriteString(speakerLine("refined diarization", r.DiarizationRefined))
		}
		b.WriteString(speakerLine("final transcript labels", run.SpeakersFinal))
		if run.SpeakersSplit != nil {
			b.WriteString(speakerLine("final, split at changes", run.SpeakersSplit))
		}
		fmt.Fprintf(&b, "Who said what      live %s", pct(run.WhoSaidWhatLive.Rate()))
		if run.WhoSaidWhatFinal != nil {
			fmt.Fprintf(&b, "   final %s", pct(run.WhoSaidWhatFinal.Rate()))
		}
		if run.WhoSaidWhatSplit != nil {
			fmt.Fprintf(&b, "   split %s", pct(run.WhoSaidWhatSplit.Rate()))
		}
		fmt.Fprintln(&b, "   (word errors, a right word with the wrong speaker counts as wrong)")
		x := run.ExchangesLive
		fmt.Fprintf(&b, "Quick exchanges    %d short turns: live found %d, right speaker %d", x.Turns, x.Found, x.RightSpeaker)
		if run.ExchangesFinal != nil {
			fmt.Fprintf(&b, "; final right speaker %d", run.ExchangesFinal.RightSpeaker)
		}
		if run.ExchangesSplit != nil {
			fmt.Fprintf(&b, "; split right speaker %d", run.ExchangesSplit.RightSpeaker)
		}
		fmt.Fprintln(&b)
		if run.OverlapsLive.Interjections > 0 {
			o := run.OverlapsLive
			fmt.Fprintf(&b, "Annotated overlap  %d interjections (%.0fs): transcribed live %d, right speaker live %d", o.Interjections, o.Seconds, o.Found, o.RightSpeaker)
			if f := run.OverlapsFinal; f != nil {
				fmt.Fprintf(&b, ", final %d", f.RightSpeaker)
			}
			if sp := run.OverlapsSplit; sp != nil {
				fmt.Fprintf(&b, ", split %d", sp.RightSpeaker)
			}
			fmt.Fprintln(&b)
			if r.OverlapsInitial != nil {
				fmt.Fprintf(&b, "                   two voices detected during them: initial diarization %.0fs, refined %.0fs of %.0fs\n",
					r.OverlapsInitial.DetectedSeconds, r.OverlapsRefined.DetectedSeconds, o.Seconds)
			}
		}
		fmt.Fprintln(&b, "Video-hint names   not scored (no hints in an offline eval yet)")
		fmt.Fprintf(&b, "Run time           pipeline %s", formatDuration(run.Seconds))
		if r.DiarizationSecs > 0 {
			fmt.Fprintf(&b, ", diarization %s (in parallel, shared by all runs)", formatDuration(r.DiarizationSecs))
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "\nWall time %s", formatDuration(r.WallSecs))
	if r.CacheHits+r.CacheMisses > 0 {
		fmt.Fprintf(&b, "; transcription cache: %d reused, %d decoded", r.CacheHits, r.CacheMisses)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "\nConfusion: share of scored speech given to the wrong person (lower is better).")
	fmt.Fprintln(&b, "Purity: how much each detected speaker is one person. Coverage: how much each person stays in one detected speaker.")
	return b.String()
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// writeEvalOutputs writes the report, scores and spot-check transcripts.
func writeEvalOutputs(dir string, r *evalReport, ref *eval.Reference, initial, refined []eval.Labeled) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	if err := write("report.txt", formatEvalReport(r)); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(r, "", "  ")
	if err := write("scores.json", string(js)); err != nil {
		return err
	}
	var refText strings.Builder
	for _, t := range ref.Turns {
		fmt.Fprintf(&refText, "[%s] %s: %s\n", formatDuration(t.Start), t.Speaker, t.Text)
	}
	if err := write("reference.txt", refText.String()); err != nil {
		return err
	}
	if initial != nil {
		if err := write("diarization-initial.txt", formatTimeline(initial)); err != nil {
			return err
		}
		if err := write("diarization-refined.txt", formatTimeline(refined)); err != nil {
			return err
		}
	}
	for _, run := range r.Runs {
		files := map[string][]eval.Labeled{"live": run.live, "final": run.final, "split": run.split, "pass1": run.pass1}
		for kind, ls := range files {
			if len(ls) == 0 {
				continue
			}
			if err := write(run.Name+"-"+kind+".txt", formatLabeled(ls)); err != nil {
				return err
			}
		}
		labels := run.final
		mapping := map[string]string{}
		if run.SpeakersFinal != nil {
			mapping = run.SpeakersFinal.Mapping
		} else {
			labels, mapping = run.live, run.SpeakersLive.Mapping
		}
		if err := write(run.Name+"-spotcheck.txt", formatSpotcheck(ref, labels, mapping)); err != nil {
			return err
		}
		if run.SpeakersSplit != nil {
			if err := write(run.Name+"-spotcheck-split.txt", formatSpotcheck(ref, run.split, run.SpeakersSplit.Mapping)); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatLabeled(ls []eval.Labeled) string {
	var b strings.Builder
	for _, l := range ls {
		fmt.Fprintf(&b, "[%s-%s] %s: %s\n", formatDuration(l.Start), formatDuration(l.End), l.Speaker, l.Text)
	}
	return b.String()
}

func formatTimeline(ls []eval.Labeled) string {
	var b strings.Builder
	for _, l := range ls {
		fmt.Fprintf(&b, "[%s-%s] %s\n", formatDuration(l.Start), formatDuration(l.End), l.Speaker)
	}
	return b.String()
}

// formatSpotcheck lays each reference turn next to the hypothesis lines
// that overlap it, with each hypothesis speaker's matched reference name,
// so a mislabel or a missed line stands out when reading through.
func formatSpotcheck(ref *eval.Reference, hyp []eval.Labeled, mapping map[string]string) string {
	var b strings.Builder
	for _, t := range ref.Turns {
		fmt.Fprintf(&b, "REF [%s] %s: %s\n", formatDuration(t.Start), t.Speaker, t.Text)
		for _, h := range hyp {
			if h.End <= t.Start || h.Start >= t.End {
				continue
			}
			who := mapping[h.Speaker]
			mark := " "
			switch {
			case who == "":
				who, mark = "unmatched", "?"
			case who != t.Speaker && h.Start >= t.Start:
				mark = "!"
			}
			fmt.Fprintf(&b, "  %s [%s] %s = %s: %s\n", mark, formatDuration(h.Start), h.Speaker, who, h.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// parseSeconds parses a duration like "10m", "90s" or "1h5m", or plain
// seconds; "" is 0.
func parseSeconds(v string) (float64, error) {
	if v == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d.Seconds(), nil
	}
	return strconv.ParseFloat(v, 64)
}
