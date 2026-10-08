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
	"unicode"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
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
           (the defaults, and how the pipeline has always behaved)
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
		var o replayOptions
		o.asrModel, _ = cmd.Flags().GetString("asr-model")
		o.decoding, _ = cmd.Flags().GetString("decoding")
		o.onlyCurrent, _ = cmd.Flags().GetBool("only-current")
		o.progress, _ = cmd.Flags().GetBool("progress")
		o.asrKind, _ = cmd.Flags().GetString("asr-kind")
		o.singlePass, _ = cmd.Flags().GetBool("single-pass")
		o.threads, _ = cmd.Flags().GetInt("threads")
		o.minSilence, _ = cmd.Flags().GetFloat64("min-silence")
		o.maxSpeech, _ = cmd.Flags().GetFloat64("max-speech")
		o.decodePad, _ = cmd.Flags().GetFloat64("decode-pad")
		if cmd.Flags().Changed("turn-mode") {
			on, _ := cmd.Flags().GetBool("turn-mode")
			o.turnMode = &on
		}
		o.turnMax, _ = cmd.Flags().GetFloat64("turn-max")
		o.turnGap, _ = cmd.Flags().GetFloat64("turn-gap")
		o.turnSignals, _ = cmd.Flags().GetString("turn-signals")
		return runSessionReplay(args[0], out, mainThreshold, o)
	},
}

// replayOptions are session replay's settings for testing transcription
// changes against a session's real audio (see tomoe textdiff).
type replayOptions struct {
	// asrModel is a Parakeet-style transducer directory (encoder, decoder,
	// joiner .onnx and tokens.txt) to transcribe with instead of the
	// configured one; decoding overrides decoding_method.
	asrModel string
	decoding string
	// onlyCurrent skips the main run; progress prints "progress ..."
	// lines as the audio is replayed.
	onlyCurrent bool
	progress    bool
	// asrKind loads asrModel as another model family (see
	// transcribe.CandidateKinds); singlePass turns two-pass off, so only
	// the model under test writes text; threads is its CPU threads.
	asrKind    string
	singlePass bool
	threads    int
	// minSilence and maxSpeech are the utterance bounds (0: the config's).
	minSilence, maxSpeech float64
	// decodePad is seconds of silence around every decode (< 0: the
	// config's decode_pad).
	decodePad float64
	// turnMode overrides the config's turn_mode when set; turnMax and
	// turnGap its limits (0: the config's). turnSignals is which
	// speaker-change signals end a turn, rebuilt from the session's saved
	// diarization windows and meeting-window looks: "diarizer", "teams",
	// "both" (as the app had them) or "none".
	turnMode         *bool
	turnMax, turnGap float64
	turnSignals      string
}

func init() {
	sessionReplayCmd.Flags().String("out", "", "Directory for main.txt/current.txt (default: replay-<session-id> in the current directory)")
	sessionReplayCmd.Flags().Float64("main-threshold", 0, "Speaker threshold for the main run (default: your speaker_threshold)")
	sessionReplayCmd.Flags().String("asr-model", "", "Transcribe with this transducer model directory (encoder/decoder/joiner .onnx + tokens.txt) instead of the configured Parakeet")
	sessionReplayCmd.Flags().String("decoding", "", "Decoding method for this replay: greedy_search or modified_beam_search (default: your decoding_method)")
	sessionReplayCmd.Flags().Bool("only-current", false, "Replay only with your current config (skip the main run)")
	sessionReplayCmd.Flags().Bool("progress", false, "Print progress lines (\"progress replay <run> <done>/<total>\") while replaying")
	sessionReplayCmd.Flags().String("asr-kind", "", "With --asr-model: the model family ("+strings.Join(transcribe.CandidateKinds, ", ")+"); default a Parakeet-style transducer")
	sessionReplayCmd.Flags().Bool("single-pass", false, "Turn two-pass off for this replay, so only the transcription model writes text")
	sessionReplayCmd.Flags().Float64("min-silence", 0, "Pause (s) that ends an utterance (default: your min_silence_duration)")
	sessionReplayCmd.Flags().Float64("max-speech", 0, "Longest utterance (s) before it's cut (default: your max_speech_duration)")
	sessionReplayCmd.Flags().Float64("decode-pad", -1, "Seconds of silence added before and after every decode (default: your decode_pad)")
	sessionReplayCmd.Flags().Bool("turn-mode", false, "Decode whole speaker turns, or each utterance with =false (default: your turn_mode)")
	sessionReplayCmd.Flags().Float64("turn-max", 0, "Turn mode: longest line in seconds (default: your turn_max_seconds)")
	sessionReplayCmd.Flags().Float64("turn-gap", 0, "Turn mode: a pause longer than this many seconds ends a turn (default: your turn_max_gap)")
	sessionReplayCmd.Flags().String("turn-signals", "both", "Turn mode: speaker-change signals that end a turn, as the app had them: diarizer, teams, both or none")
	sessionReplayCmd.Flags().Int("threads", 0, "CPU threads for the transcription model (default: the engine's)")
	sessionCmd.AddCommand(sessionReplayCmd)
}

// replayRun is one pipeline configuration to replay a session with.
type replayRun struct {
	name     string
	twoPass  bool
	tuning   speaker.Tuning
	segments []session.Segment
	drafts   []session.Segment
}

func runSessionReplay(sessID, outDir string, mainThreshold float64, o replayOptions) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	status := models.NewManager(cfg.Transcription.ModelPath).Check()
	if !status.Ready() {
		return fmt.Errorf("transcription models not downloaded (run 'tomoe model download')")
	}
	if o.asrModel != "" && o.asrKind == "" {
		if err := useTransducerDir(status, o.asrModel); err != nil {
			return err
		}
		// Use exactly these files: the model setting would otherwise pick
		// its own for English (see models.ASRModelFor).
		cfg.Transcription.Model = models.ASRModelParakeetV3
		fmt.Printf("Transcribing with %s\n", o.asrModel)
	}
	if o.decoding != "" {
		cfg.Transcription.DecodingMethod = o.decoding
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
	cur := &replayRun{name: "current", twoPass: cfg.Transcription.TwoPass && lang == "en" && !o.singlePass, tuning: current}
	runs := []*replayRun{{name: "main", twoPass: false, tuning: baseline}, cur}
	if o.onlyCurrent {
		runs = []*replayRun{cur}
	}

	pipe, err := loadOfflinePipeline(cfg, status, lang, monitor != nil, cur.twoPass)
	if err != nil {
		return err
	}
	defer pipe.Close()
	if pipe.streaming == nil {
		cur.twoPass = false
	}
	timed := &timedEngine{}
	if o.asrKind != "" {
		eng, err := transcribe.NewCandidateEngine(o.asrKind, o.asrModel, o.threads)
		if err != nil {
			return err
		}
		defer eng.Close()
		pipe.engine = eng
		fmt.Printf("Transcribing with %s model %s\n", o.asrKind, o.asrModel)
	}
	timed.Engine = pipe.engine
	pipe.engine = timed

	fmt.Printf("Replaying %q (%s of audio)...\n", sess.Title, formatDuration(float64(max(len(mic), len(monitor)))/16000))
	signals := speakerChangeSignals(sess, o.turnSignals)
	for _, run := range runs {
		lc := pipe.liveConfig(run.tuning, run.twoPass)
		lc.MinSpeechLevelDB, lc.MicLevelMarginDB = cfg.Meeting.MinSpeechLevelDB, cfg.Meeting.MicLevelMarginDB
		lc.TurnMode, lc.TurnMaxSeconds, lc.TurnMaxGap = cfg.Meeting.TurnMode, cfg.Meeting.TurnMaxSeconds, cfg.Meeting.TurnMaxGap
		if o.turnMode != nil {
			lc.TurnMode = *o.turnMode
		}
		if o.turnMax > 0 {
			lc.TurnMaxSeconds = o.turnMax
		}
		if o.turnGap > 0 {
			lc.TurnMaxGap = o.turnGap
		}
		if lc.TurnMode {
			lc.SpeakerChanged = signals
		}
		lc.MinSilenceDuration, lc.MaxSpeechDuration = cfg.Meeting.MinSilenceDuration, cfg.Meeting.MaxSpeechDuration
		if o.minSilence > 0 {
			lc.MinSilenceDuration = o.minSilence
		}
		if o.maxSpeech > 0 {
			lc.MaxSpeechDuration = o.maxSpeech
		}
		if o.progress {
			name := run.name
			lc.ReplayProgress = func(done, total int) { fmt.Printf("progress replay %s %d/%d\n", name, done, total) }
		}
		lc.DecodePad = cfg.Meeting.DecodePad
		if o.decodePad >= 0 {
			lc.DecodePad = o.decodePad
		}
		res, err := live.ReplayDetailed(lc, mic, monitor)
		if err != nil {
			return fmt.Errorf("%s run: %w", run.name, err)
		}
		run.segments, run.drafts = res.Segments, res.Drafts
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
		// The segments with word timings, for tomoe textdiff.
		js, _ := json.MarshalIndent(run.segments, "", " ")
		if err := os.WriteFile(filepath.Join(outDir, run.name+".json"), js, 0o644); err != nil {
			return fmt.Errorf("writing %s.json: %w", run.name, err)
		}
		if len(run.drafts) > 0 {
			// The live (pass-1) lines as first shown, for comparing drafts
			// with the final text.
			js, _ := json.MarshalIndent(run.drafts, "", " ")
			if err := os.WriteFile(filepath.Join(outDir, run.name+".drafts.json"), js, 0o644); err != nil {
				return fmt.Errorf("writing %s.drafts.json: %w", run.name, err)
			}
		}
	}

	if len(runs) == 2 {
		printReplaySummary(runs)
	}
	audio := float64(max(len(mic), len(monitor))) / 16000
	fmt.Printf("\nDecode time: %.1fs for %d utterances (%.1f%% of the audio's length)\n", timed.secs, timed.n, 100*timed.secs/max(1, audio))
	fmt.Printf("Transcripts in %s\n", outDir)
	return nil
}

// useTransducerDir points status's transcription model at dir: its
// encoder, decoder and joiner .onnx files (the int8 ones if there are
// both) and tokens.txt.
func useTransducerDir(status *models.Status, dir string) error {
	find := func(part string) (string, error) {
		var plain, int8 string
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, part) || !strings.HasSuffix(n, ".onnx") {
				continue
			}
			if strings.Contains(n, "int8") {
				int8 = filepath.Join(dir, n)
			} else {
				plain = filepath.Join(dir, n)
			}
		}
		if int8 != "" {
			return int8, nil
		}
		if plain != "" {
			return plain, nil
		}
		return "", fmt.Errorf("no %s*.onnx in %s", part, dir)
	}
	var err error
	if status.EncoderPath, err = find("encoder"); err != nil {
		return err
	}
	if status.DecoderPath, err = find("decoder"); err != nil {
		return err
	}
	if status.JoinerPath, err = find("joiner"); err != nil {
		return err
	}
	status.TokensPath = filepath.Join(dir, "tokens.txt")
	if _, err := os.Stat(status.TokensPath); err != nil {
		return fmt.Errorf("no tokens.txt in %s", dir)
	}
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

// timedEngine adds up the time spent in TranscribeDirect, for comparing
// models' speed in a replay.
type timedEngine struct {
	transcribe.Engine
	secs float64
	n    int
}

func (t *timedEngine) TranscribeDirect(samples []float32) (*transcribe.Result, error) {
	began := time.Now()
	r, err := t.Engine.TranscribeDirect(samples)
	t.secs += time.Since(began).Seconds()
	t.n++
	return r, err
}

// speakerChangeSignals is live.Config.SpeakerChanged as the app would have
// had it for sess: new-voice times from its saved diarization windows
// and/or the moments the meeting window's highlighted name changed.
func speakerChangeSignals(sess *session.Session, which string) func(from, to float64) bool {
	var times []float64
	dir := filepath.Join(config.SessionDir(), sess.ID)
	if which == "diarizer" || which == "both" {
		if prep, info, err := loadFingerprints(dir); err == nil {
			for _, t := range prep.NewVoiceTimes() {
				times = append(times, t+info.Offset)
			}
		}
	}
	if which == "teams" || which == "both" {
		if looks, err := videohint.ReadLooks(dir); err == nil {
			last := ""
			for _, l := range looks {
				name, _ := l.Accepted()
				if name == "" {
					continue
				}
				if last != "" && name != last {
					times = append(times, l.Time.Sub(sess.CreatedAt).Seconds())
				}
				last = name
			}
		}
	}
	sort.Float64s(times)
	fmt.Printf("Turn signals (%s): %d\n", which, len(times))
	return func(from, to float64) bool {
		i := sort.SearchFloat64s(times, from)
		return i < len(times) && times[i] <= to
	}
}
