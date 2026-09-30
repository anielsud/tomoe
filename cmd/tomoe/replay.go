package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// sessionReplayCmd re-runs a saved session's recorded audio through the live
// pipeline twice, once as upstream main's pipeline behaved and once with the
// current config, and compares the two transcripts. It's the way to check a
// pipeline or clustering change against real recordings instead of guessing.
var sessionReplayCmd = &cobra.Command{
	Use:   "replay <session-id>",
	Short: "Replay a session's audio through the pipeline and compare main's behavior with the current config",
	Long: `Replays a saved session's audio through the live transcription pipeline
twice and compares the results:

  main     single-pass, no sticky-speaker or short-segment rules
           (how the pipeline behaved before the macOS port)
  current  your config.toml as it is now

Both runs use your speaker_threshold unless --main-threshold is given, so
the comparison isolates the pipeline and clustering rules. Transcripts are
written to --out as main.txt and current.txt for diffing.

The saved audio is AAC-compressed, so neither run is identical to what
was heard live, but both runs hear the same audio.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out, _ := cmd.Flags().GetString("out")
		mainThreshold, _ := cmd.Flags().GetFloat64("main-threshold")
		return runSessionReplay(args[0], out, mainThreshold)
	},
}

func init() {
	sessionReplayCmd.Flags().String("out", "", "Directory for main.txt/current.txt (default: replay-<session-id> in the current directory)")
	sessionReplayCmd.Flags().Float64("main-threshold", 0, "Speaker threshold for the main run (default: your speaker_threshold)")
	sessionCmd.AddCommand(sessionReplayCmd)
}

// replayRun is one pipeline configuration to replay a session with.
type replayRun struct {
	name     string
	twoPass  bool
	tuning   speaker.Tuning
	segments []session.Segment
}

func runSessionReplay(sessID, outDir string, mainThreshold float64) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	status := models.NewManager(cfg.Transcription.ModelPath).Check()
	if !status.Ready() {
		return fmt.Errorf("transcription models not downloaded (run 'tomoe model download')")
	}

	sess, err := session.NewStore(config.SessionDir()).Load(sessID)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}
	if sess.AudioPath == "" {
		return fmt.Errorf("session %q has no recorded audio", sess.Title)
	}
	mic, monitor, err := loadReplayTracks(sess)
	if err != nil {
		return err
	}
	lang := sess.Language
	if lang == "" {
		lang = "en"
	}

	engines, err := transcribe.NewEngineSetFromConfig(transcribe.Config{
		EncoderPath:    status.EncoderPath,
		DecoderPath:    status.DecoderPath,
		JoinerPath:     status.JoinerPath,
		TokensPath:     status.TokensPath,
		VADPath:        status.VADPath,
		UseGPU:         cfg.Transcription.GPUEnabled,
		DecodingMethod: cfg.Transcription.DecodingMethod,
		MaxActivePaths: cfg.Transcription.MaxActivePaths,
		HotwordsFile:   cfg.Transcription.HotwordsFile,
		HotwordsScore:  cfg.Transcription.HotwordsScore,
	}, status, &cfg.Multilingual)
	if err != nil {
		return fmt.Errorf("creating transcription engine: %w", err)
	}
	defer engines.Close()
	engine := engines.Get(lang)
	if engine == nil {
		return fmt.Errorf("no transcription engine for language %q", lang)
	}

	var embedder *speaker.Embedder
	if monitor != nil && status.SpeakerEmbeddingReady {
		if embedder, err = speaker.NewEmbedder(status.SpeakerEmbeddingPath); err != nil {
			return fmt.Errorf("loading speaker embedding model: %w", err)
		}
		defer embedder.Close()
	}

	current := speaker.TuningFromSeconds(
		cfg.Meeting.SpeakerThreshold,
		cfg.Meeting.StickyGraceWindow,
		cfg.Meeting.StickyThresholdMargin,
		cfg.Meeting.MinAssignDuration,
		cfg.Meeting.ShortSegmentGraceWindow,
	)
	baseline := current
	baseline.StickyThresholdMargin = 0
	baseline.MinAssignDuration = 0
	if mainThreshold > 0 {
		baseline.Threshold = mainThreshold
	}
	runs := []*replayRun{
		{name: "main", twoPass: false, tuning: baseline},
		{name: "current", twoPass: cfg.Transcription.TwoPass && lang == "en", tuning: current},
	}

	var streaming transcribe.StreamingEngine
	if runs[1].twoPass {
		if !status.EnglishStreamingReady {
			fmt.Println("Note: English streaming model not downloaded; the current run is single-pass.")
			runs[1].twoPass = false
		} else {
			streaming, err = transcribe.NewStreamingEngine(transcribe.StreamingConfig{
				EncoderPath: status.EnglishStreamingEncoderPath,
				DecoderPath: status.EnglishStreamingDecoderPath,
				JoinerPath:  status.EnglishStreamingJoinerPath,
				TokensPath:  status.EnglishStreamingTokensPath,
			})
			if err != nil {
				return fmt.Errorf("loading English streaming model: %w", err)
			}
			defer streaming.Close()
		}
	}

	fmt.Printf("Replaying %q (%s of audio)...\n", sess.Title, formatDuration(float64(max(len(mic), len(monitor)))/16000))
	for _, run := range runs {
		lc := live.Config{Engine: engine, VADPath: status.VADPath}
		if embedder != nil {
			tracker := speaker.NewTracker(run.tuning.Threshold)
			tracker.SetTuning(run.tuning)
			lc.Embedder, lc.Tracker = embedder, tracker
		}
		if run.twoPass {
			lc.StreamingEngine = streaming
		}
		if run.segments, err = live.Replay(lc, mic, monitor); err != nil {
			return fmt.Errorf("%s run: %w", run.name, err)
		}
		sort.SliceStable(run.segments, func(i, j int) bool { return run.segments[i].StartTime < run.segments[j].StartTime })
	}

	if outDir == "" {
		outDir = "replay-" + sess.ID
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	for _, run := range runs {
		path := filepath.Join(outDir, run.name+".txt")
		if err := os.WriteFile(path, []byte(formatReplayTranscript(run.segments)), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}

	printReplaySummary(runs)
	fmt.Printf("\nTranscripts: %s, %s\n", filepath.Join(outDir, "main.txt"), filepath.Join(outDir, "current.txt"))
	return nil
}

// loadReplayTracks decodes a session's audio into its mic and monitor
// tracks. A dual-source session's M4A holds a mixed track, then mic, then
// monitor (see session.SaveAudioM4A); a single-source session holds one
// track, whose source the session records.
func loadReplayTracks(sess *session.Session) (mic, monitor []float32, err error) {
	if len(sess.Sources) >= 2 {
		if mic, err = session.DecodeTrackToFloat32(sess.AudioPath, 1); err != nil {
			return nil, nil, fmt.Errorf("decoding mic track: %w", err)
		}
		if monitor, err = session.DecodeTrackToFloat32(sess.AudioPath, 2); err != nil {
			return nil, nil, fmt.Errorf("decoding monitor track: %w", err)
		}
		return mic, monitor, nil
	}
	samples, err := session.DecodeTrackToFloat32(sess.AudioPath, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding audio: %w", err)
	}
	source := "mic"
	if len(sess.Sources) == 1 {
		source = sess.Sources[0]
	} else if len(sess.Segments) > 0 {
		source = sess.Segments[0].Source
	}
	if source == string(live.SourceMonitor) {
		return nil, samples, nil
	}
	return samples, nil, nil
}

func formatReplayTranscript(segs []session.Segment) string {
	var b strings.Builder
	for _, s := range segs {
		fmt.Fprintf(&b, "[%s] %s: %s\n", formatDuration(s.StartTime), s.Speaker, s.Text)
	}
	return b.String()
}

func formatDuration(sec float64) string {
	total := int(sec)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, total/60%60, total%60)
}

func printReplaySummary(runs []*replayRun) {
	fmt.Println()
	fmt.Printf("%-8s  %8s  %8s  %13s\n", "run", "segments", "words", "monitor spkrs")
	for _, run := range runs {
		fmt.Printf("%-8s  %8d  %8d  %13d\n", run.name, len(run.segments), len(transcriptWords(run.segments, "")), countSpeakers(run.segments))
	}

	a, b := runs[0].segments, runs[1].segments
	fmt.Println()
	for _, source := range []string{string(live.SourceMic), string(live.SourceMonitor)} {
		wa, wb := transcriptWords(a, source), transcriptWords(b, source)
		if len(wa) == 0 && len(wb) == 0 {
			continue
		}
		d := wordDistance(wa, wb)
		fmt.Printf("%s text: %d word edits between runs (%.1f%% of main's %d words)\n",
			source, d, 100*float64(d)/math.Max(1, float64(len(wa))), len(wa))
	}
	if agreed, total := speakerAgreement(a, b); total > 0 {
		fmt.Printf("monitor speakers: same speaker in both runs for %.1f%% of speech (%s of %s)\n",
			100*agreed/total, formatDuration(agreed), formatDuration(total))
	}
}

// transcriptWords returns the normalized words (lowercase, punctuation
// stripped) of segs from source, or of all sources if source is "".
func transcriptWords(segs []session.Segment, source string) []string {
	var words []string
	for _, s := range segs {
		if source != "" && s.Source != source {
			continue
		}
		for _, w := range strings.Fields(strings.ToLower(s.Text)) {
			if w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }); w != "" {
				words = append(words, w)
			}
		}
	}
	return words
}

// wordDistance is the word-level Levenshtein distance between a and b.
func wordDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func countSpeakers(segs []session.Segment) int {
	seen := make(map[string]bool)
	for _, s := range segs {
		if s.Source == string(live.SourceMonitor) {
			seen[s.Speaker] = true
		}
	}
	return len(seen)
}

// speakerAgreement measures how much of a's monitor speech (in seconds)
// gets the same speaker in b. Labels are arbitrary per run ("Person 2" in
// one can be "Person 3" in the other), so a's labels are matched one-to-one
// to b's, greedily by how long they overlap in time, and agreement is the
// overlap along those matched pairs.
func speakerAgreement(a, b []session.Segment) (agreed, total float64) {
	type pair struct{ la, lb string }
	overlap := make(map[pair]float64)
	for _, sa := range a {
		if sa.Source != string(live.SourceMonitor) {
			continue
		}
		total += sa.EndTime - sa.StartTime
		for _, sb := range b {
			if sb.Source != string(live.SourceMonitor) {
				continue
			}
			if o := math.Min(sa.EndTime, sb.EndTime) - math.Max(sa.StartTime, sb.StartTime); o > 0 {
				overlap[pair{sa.Speaker, sb.Speaker}] += o
			}
		}
	}

	pairs := make([]pair, 0, len(overlap))
	for p := range overlap {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if overlap[pairs[i]] != overlap[pairs[j]] {
			return overlap[pairs[i]] > overlap[pairs[j]]
		}
		return pairs[i].la+"\x00"+pairs[i].lb < pairs[j].la+"\x00"+pairs[j].lb
	})
	usedA, usedB := make(map[string]bool), make(map[string]bool)
	for _, p := range pairs {
		if usedA[p.la] || usedB[p.lb] {
			continue
		}
		usedA[p.la], usedB[p.lb] = true, true
		agreed += overlap[p]
	}
	return agreed, total
}
