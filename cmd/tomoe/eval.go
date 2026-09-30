package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		return runEval(opts)
	},
}

func init() {
	evalCmd.Flags().String("ref", "", "Reference transcript (Teams transcript export as .txt)")
	evalCmd.Flags().String("out", "", "Output directory (default: eval-<media name> in the current directory)")
	evalCmd.Flags().Float64("collar", 1.0, "Seconds around each reference speaker change not scored (Teams timestamps are to the second)")
	evalCmd.Flags().Bool("skip-diarization", false, "Skip the post-meeting diarization passes (much faster)")
	evalCmd.Flags().StringSlice("runs", []string{"default", "experimental"}, "Pipeline settings to run: default, experimental")
	_ = evalCmd.MarkFlagRequired("ref")
	rootCmd.AddCommand(evalCmd)
}

type evalOptions struct {
	media, ref, out string
	collar          float64
	skipDiarization bool
	runs            []string
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

	Seconds float64 `json:"run_seconds"`

	live, final []eval.Labeled
	pass1       []eval.Labeled
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

	DiarizationInitial *eval.SpeakerScore `json:"diarization_initial,omitempty"`
	DiarizationRefined *eval.SpeakerScore `json:"diarization_refined,omitempty"`
	DiarizationSecs    float64            `json:"diarization_seconds,omitempty"`

	Runs []*evalRun `json:"runs"`
}

func runEval(opts evalOptions) error {
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
	audioSecs := float64(len(samples)) / 16000

	f, err := os.Open(opts.ref)
	if err != nil {
		return err
	}
	ref, err := eval.ParseTeamsTranscript(f, audioSecs)
	f.Close()
	if err != nil {
		return fmt.Errorf("parsing reference: %w", err)
	}
	var refWords []string
	for _, t := range ref.Turns {
		refWords = append(refWords, eval.Words(t.Text)...)
	}

	report := &evalReport{
		Media: filepath.Base(opts.media), Reference: filepath.Base(opts.ref), AudioSecs: audioSecs,
		RefTurns: len(ref.Turns), RefSpeakers: len(ref.Speakers()), RefWords: len(refWords), Collar: opts.collar,
	}
	fmt.Printf("Reference: %d turns, %d speakers, %d words over %s\n", report.RefTurns, report.RefSpeakers, report.RefWords, formatDuration(audioSecs))

	runs, err := evalRuns(opts.runs)
	if err != nil {
		return err
	}
	wantStreaming := false
	for _, r := range runs {
		wantStreaming = wantStreaming || r.TwoPass
	}
	pipe, err := loadOfflinePipeline(cfg, status, "en", true, wantStreaming)
	if err != nil {
		return err
	}
	defer pipe.Close()

	for _, run := range runs {
		if run.TwoPass && pipe.streaming == nil {
			run.TwoPass = false
		}
		fmt.Printf("Running %s pipeline (%s)...\n", run.Name, run.Tuning)
		began := time.Now()
		res, err := live.ReplayDetailed(pipe.liveConfig(tuningFor(run.Name), run.TwoPass), nil, samples)
		if err != nil {
			return fmt.Errorf("%s run: %w", run.Name, err)
		}
		run.Seconds = time.Since(began).Seconds()
		for _, s := range res.Segments {
			run.live = append(run.live, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: s.Text})
			if p1, ok := res.Pass1Text[s.ID]; ok {
				run.pass1 = append(run.pass1, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: p1})
			}
		}
		sortLabeled(run.live)
		sortLabeled(run.pass1)
		scoreRun(run, ref, refWords, opts.collar)
		fmt.Printf("  done in %s\n", formatDuration(run.Seconds))
	}

	var initial, refined []eval.Labeled
	if !opts.skipDiarization {
		fmt.Println("Running post-meeting diarization (this is the slow part)...")
		began := time.Now()
		dcfg := session.DiarizeConfig{
			SegmentationModelPath: status.SpeakerSegmentationPath,
			EmbeddingModelPath:    status.SpeakerEmbeddingPath,
			Threshold:             1.1, // same settings as the post-save diarization (cmd/tomoe/diarize.go)
			UseGPU:                cfg.Transcription.GPUEnabled,
		}
		rawSegs, rawMap, err := session.Diarize(samples, dcfg)
		if err != nil {
			return fmt.Errorf("diarization: %w", err)
		}
		mergedSegs, mergedMap := session.MergeSimilarSpeakers(append([]session.DiarizeSegment(nil), rawSegs...), copyMap(rawMap), samples, status.SpeakerEmbeddingPath, 0.55, false)
		report.DiarizationSecs = time.Since(began).Seconds()
		initial = diarLabeled(rawSegs, rawMap)
		refined = diarLabeled(mergedSegs, mergedMap)
		si := eval.ScoreSpeakers(ref, initial, opts.collar)
		sr := eval.ScoreSpeakers(ref, refined, opts.collar)
		report.DiarizationInitial, report.DiarizationRefined = &si, &sr

		for _, run := range runs {
			segs := make([]session.Segment, len(run.live))
			for i, l := range run.live {
				segs[i] = session.Segment{StartTime: l.Start, EndTime: l.End, Speaker: l.Speaker, Text: l.Text, Source: "monitor"}
			}
			session.RelabelByDiarization(segs, mergedSegs, mergedMap)
			for _, s := range segs {
				run.final = append(run.final, eval.Labeled{Start: s.StartTime, End: s.EndTime, Speaker: s.Speaker, Text: s.Text})
			}
			sf := eval.ScoreSpeakers(ref, run.final, opts.collar)
			wf := eval.SpeakerAttributedErrors(ref, run.final, sf.Mapping)
			xf := eval.ScoreQuickExchanges(ref, run.final, sf.Mapping, 8, 6, 3)
			run.SpeakersFinal, run.WhoSaidWhatFinal, run.ExchangesFinal = &sf, &wf, &xf
		}
		fmt.Printf("  done in %s\n", formatDuration(report.DiarizationSecs))
	}
	report.Runs = runs

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
	fmt.Fprintln(&b, "Text scores are disagreement with the Teams transcript (itself automatic), not true accuracy.")
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
		fmt.Fprintf(&b, "Who said what      live %s", pct(run.WhoSaidWhatLive.Rate()))
		if run.WhoSaidWhatFinal != nil {
			fmt.Fprintf(&b, "   final %s", pct(run.WhoSaidWhatFinal.Rate()))
		}
		fmt.Fprintln(&b, "   (word errors, a right word with the wrong speaker counts as wrong)")
		x := run.ExchangesLive
		fmt.Fprintf(&b, "Quick exchanges    %d short turns: live found %d, right speaker %d", x.Turns, x.Found, x.RightSpeaker)
		if run.ExchangesFinal != nil {
			fmt.Fprintf(&b, "; final right speaker %d", run.ExchangesFinal.RightSpeaker)
		}
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Video-hint names   not scored (no hints in an offline eval yet)")
		fmt.Fprintf(&b, "Run time           %s", formatDuration(run.Seconds))
		if r.DiarizationSecs > 0 {
			fmt.Fprintf(&b, " + diarization %s (shared by all runs)", formatDuration(r.DiarizationSecs))
		}
		fmt.Fprintln(&b)
	}
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
		files := map[string][]eval.Labeled{"live": run.live, "final": run.final, "pass1": run.pass1}
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
