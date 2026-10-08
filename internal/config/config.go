package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config is the top-level configuration, mapped to ~/.config/tomoe/config.toml.
type Config struct {
	Hotkey        HotkeyConfig        `toml:"hotkey"`
	Audio         AudioConfig         `toml:"audio"`
	Transcription TranscriptionConfig `toml:"transcription"`
	Output        OutputConfig        `toml:"output"`
	Meeting       MeetingConfig       `toml:"meeting"`
	Multilingual  MultilingualConfig  `toml:"multilingual"`
}

// HotkeyConfig holds global hotkey settings.
type HotkeyConfig struct {
	Binding        string `toml:"binding"`
	MeetingBinding string `toml:"meeting_binding"`
}

// AudioConfig holds audio capture settings.
type AudioConfig struct {
	Device string `toml:"device"`
}

// TranscriptionConfig holds transcription engine settings.
type TranscriptionConfig struct {
	GPUEnabled     bool    `toml:"gpu_enabled"`
	ModelPath      string  `toml:"model_path"`
	HotwordsFile   string  `toml:"hotwords_file"`
	HotwordsScore  float32 `toml:"hotwords_score"`
	DecodingMethod string  `toml:"decoding_method"` // "greedy_search" or "modified_beam_search"
	MaxActivePaths int     `toml:"max_active_paths"`
	// TwoPass enables two-pass live transcription for English meetings
	// when the English streaming model is downloaded: streaming text as
	// it's spoken, refined by Parakeet once each utterance ends. false
	// keeps single-pass (Parakeet only, text appears per utterance).
	TwoPass bool `toml:"two_pass"`
	// Model is the transcription model: "auto" (the default) uses Cohere
	// Transcribe for English, the most accurate there of the models
	// compared, and the multilingual Parakeet v3 for other languages;
	// "parakeet-v2-en" or "parakeet-v3" pick those instead, and
	// "parakeet-v3" restores the previous behavior (models.ASRModels).
	Model string `toml:"model"`
	// LiveModel is the streaming model for the live text shown while
	// people speak (two_pass). "auto" is NeMo's streaming FastConformer:
	// on two meetings it differed from the final text on 25-38% of words
	// against 41-52% for the previous LibriSpeech Zipformer, for the same
	// CPU and ~275 MB more memory. "zipformer-2023" restores the previous
	// model; "nemotron-560" is closer still (18-31%) but needs ~1.9 GB and
	// three times the CPU (models.LiveModels).
	LiveModel string `toml:"live_model"`
}

// OutputConfig holds output behavior settings.
type OutputConfig struct {
	AutoPaste      bool    `toml:"auto_paste"`
	Clipboard      bool    `toml:"clipboard"`
	SilenceTimeout float64 `toml:"silence_timeout"` // auto-stop dictation after N seconds of silence (0=disabled)
}

// MultilingualConfig holds multilingual transcription settings.
type MultilingualConfig struct {
	Enabled     bool     `toml:"enabled"`
	Languages   []string `toml:"languages"`    // e.g. ["en", "bn"]
	DefaultLang string   `toml:"default_lang"` // fallback language: "en"
}

// MeetingConfig holds Phase 2 meeting transcription settings.
//
// The speaker-clustering and video-hint-timing fields below (from
// SpeakerThreshold down) are hot-reloaded: internal/backend and
// internal/daemon both watch config.toml's mtime and re-apply these
// live via speaker.Tracker.SetTuning, so they can be retuned without a
// rebuild or even a relaunch — added after a real live-tuning session
// (see speaker.DefaultThreshold's doc comment) needed several
// rebuild+relaunch cycles just to test one constant change at a time.
type MeetingConfig struct {
	DefaultSources     string  `toml:"default_sources"`     // "mic", "monitor", "both"
	MonitorDevice      string  `toml:"monitor_device"`      // monitor source device name; "" = default monitor (Linux), "none" = mic only
	SpeakerThreshold   float64 `toml:"speaker_threshold"`   // cosine similarity threshold for a confident speaker match
	MaxSpeechDuration  float64 `toml:"max_speech_duration"` // seconds
	MinSilenceDuration float64 `toml:"min_silence_duration"`
	// MinSpeechLevelDB drops utterances quieter than this (RMS dBFS)
	// before transcription: room noise the speech detector mistakes for
	// speech, which the model fills with invented words. 0 keeps all.
	MinSpeechLevelDB float64 `toml:"min_speech_level_db"`
	// MicLevelMarginDB drops mic utterances more than this many dB below
	// the user's typical level on their own microphone (background
	// sounds, people further away). 0 turns it off.
	MicLevelMarginDB float64 `toml:"mic_level_margin_db"`
	// TurnMode transcribes a speaker's turn at once instead of each
	// pause-separated utterance: lines keep growing across pauses until
	// the other side speaks, the diarizer or meeting window signals a
	// speaker change, a pause passes TurnMaxGap seconds or the line would
	// pass TurnMaxSeconds. More context per decode (13% more judged
	// errors fixed on five reference meetings) for about 0.3 points of
	// speaker accuracy; false restores one line per utterance.
	TurnMode       bool    `toml:"turn_mode"`
	TurnMaxSeconds float64 `toml:"turn_max_seconds"`
	TurnMaxGap     float64 `toml:"turn_max_gap"` // seconds
	AutoSave       bool    `toml:"auto_save"`    // save session on stop
	AutoDetect     bool    `toml:"auto_detect"`  // auto-detect meetings: an app using mic and speaker at once (PulseAudio on Linux, CoreAudio on macOS)

	// StickyGraceWindow/StickyThresholdMargin/MinAssignDuration/
	// ShortSegmentGraceWindow mirror speaker.Tuning's fields exactly
	// (seconds here instead of time.Duration, since TOML has no
	// duration type) — see speaker.Tuning's doc comment for what each
	// one does.
	StickyGraceWindow       float64 `toml:"sticky_grace_window"`
	StickyThresholdMargin   float64 `toml:"sticky_threshold_margin"`
	MinAssignDuration       float64 `toml:"min_assign_duration"`
	ShortSegmentGraceWindow float64 `toml:"short_segment_grace_window"`

	// SplitOnSpeakerChange makes the post-meeting diarization pass split a
	// transcript line wherever the speaker changes mid-line (a quick
	// "Right." from someone else), instead of giving the whole line one
	// speaker. Off by default like main until proven on real recordings
	// (`tomoe eval` scores both).
	SplitOnSpeakerChange bool `toml:"split_on_speaker_change"`

	// MinSpeakerWords gives the lines of any diarized speaker who says
	// fewer words than this in the whole meeting to a neighbouring
	// speaker when diarizing during the meeting: such "speakers" are
	// diarization flickers, each shown as its own "Person N" line (see
	// session.AbsorbSmallSpeakers). 0 keeps every speaker, as before.
	MinSpeakerWords int `toml:"min_speaker_words"`
	// MinSpeakerSeconds does the same by voice, first: a diarized speaker
	// heard for less than this in the whole meeting joins the speaker
	// whose voice is most like theirs (diarize.Prepared.AbsorbSmallClusters).
	// Final labels only, when diarizing during the meeting. 0 turns it off.
	MinSpeakerSeconds float64 `toml:"min_speaker_seconds"`

	// SpeakerModel is the speaker embedding model used to tell voices
	// apart, live and after the meeting: a models.SpeakerModels ID, or
	// "auto" for the English-trained model in English meetings and the
	// base model otherwise.
	SpeakerModel string `toml:"speaker_model"`

	// DiarizeDuringMeeting runs Tomoe's own diarizer while the meeting is
	// recorded instead of sherpa-onnx's after it: labels improve as the
	// meeting goes on and the final ones are ready when it ends (see
	// docs/speaker-pipeline-design.md). On by default; false restores the
	// pass after the meeting exactly as before (sherpa-onnx in a
	// subprocess), and it falls back to that by itself if the in-process
	// diarizer can't load.
	// DiarizeStride fingerprints every nth analysis window (the CPU
	// budget) and DiarizeRecluster is the seconds of audio between
	// reclusters.
	DiarizeDuringMeeting bool    `toml:"diarize_during_meeting"`
	DiarizeStride        int     `toml:"diarize_stride"`
	DiarizeRecluster     float64 `toml:"diarize_recluster"`

	// VideoHintLearnInterval is the time between looks at the meeting
	// window while a speaker still needs naming or right after a speaker
	// change, and VideoHintCheckInterval the time between looks that only
	// confirm known names (seconds; see videohint.Watcher). Both default
	// to 1 s: on five tuned meetings, looking every 1 s named the saved
	// transcript within 0.1 points of every 0.35 s for about a third of the
	// CPU (docs/speaker-attribution-research.md). 0.35 restores the old
	// learning rate, which RecordForTuning still uses.
	// VideoHintWindow is which window video hints watch: "" finds the
	// Teams or Zoom meeting window, "none" turns them off, anything else is an
	// app's name (its largest window). An app without a rule is still
	// captured, for writing one from its saved frames.
	VideoHintWindow string `toml:"video_hint_window"`

	VideoHintLearnInterval float64 `toml:"video_hint_learn_interval"`
	VideoHintCheckInterval float64 `toml:"video_hint_check_interval"`

	// RecordForTuning records meetings at full detail for working out
	// the best settings offline (`tomoe eval --session`): video hints
	// look every tuningLookInterval (or the learning rate, if faster)
	// throughout, so sparser rates can be simulated, keep every distinct
	// full frame (about 250 MB an hour), and diarization fingerprints
	// every window. Off by default; costs more CPU and disk while on.
	RecordForTuning bool `toml:"record_for_tuning"`
}

// VideoHintsOn reports whether video hints run: on macOS (the only platform
// that captures the meeting window) unless switched off. Its text-reading
// models are only fetched then.
func (m MeetingConfig) VideoHintsOn() bool {
	return runtime.GOOS == "darwin" && m.VideoHintWindow != "none"
}

// tuningLookInterval is the look rate (seconds) RecordForTuning records
// at: replays can thin a dense recording to any sparser rate, not the
// reverse.
const tuningLookInterval = 0.35

// VideoHintTiming is VideoHintLearnInterval and VideoHintCheckInterval
// as durations, with defaults for unset values; while RecordForTuning,
// learning is at most tuningLookInterval.
func (m MeetingConfig) VideoHintTiming() (learn, check time.Duration) {
	def := DefaultConfig().Meeting
	l, c := m.VideoHintLearnInterval, m.VideoHintCheckInterval
	if l <= 0 {
		l = def.VideoHintLearnInterval
	}
	if c <= 0 {
		c = def.VideoHintCheckInterval
	}
	if m.RecordForTuning && l > tuningLookInterval {
		l = tuningLookInterval
	}
	return time.Duration(l * float64(time.Second)), time.Duration(c * float64(time.Second))
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Hotkey: HotkeyConfig{
			Binding:        "Super+Shift+S",
			MeetingBinding: "Super+Shift+X",
		},
		Audio: AudioConfig{
			Device: "default",
		},
		Transcription: TranscriptionConfig{
			GPUEnabled:     false,
			ModelPath:      ModelDir(),
			DecodingMethod: "greedy_search",
			HotwordsScore:  1.5,
			MaxActivePaths: 4,
			// Off, as on main: two-pass is opt-in until it has been
			// tuned on real recordings (see `tomoe session replay`).
			TwoPass:   false,
			Model:     "auto",
			LiveModel: "auto",
		},
		Output: OutputConfig{
			AutoPaste:      true,
			Clipboard:      true,
			SilenceTimeout: 5.0,
		},
		Multilingual: MultilingualConfig{
			Enabled:     false,
			Languages:   []string{"en"},
			DefaultLang: "en",
		},
		Meeting: MeetingConfig{
			DefaultSources:       "both",
			SpeakerThreshold:     0.65,
			MaxSpeechDuration:    30.0,
			MinSilenceDuration:   0.5,
			MinSpeechLevelDB:     -50,
			MicLevelMarginDB:     20,
			TurnMode:             true,
			TurnMaxSeconds:       30,
			TurnMaxGap:           2,
			AutoSave:             true,
			AutoDetect:           true,
			SpeakerModel:         "auto",
			DiarizeDuringMeeting: true,
			DiarizeStride:        2,
			DiarizeRecluster:     10,
			MinSpeakerWords:      20,
			MinSpeakerSeconds:    2,

			// The sticky-speaker and short-segment rules are off (margin
			// and duration 0), matching main's clustering, until they're
			// proven on real recordings. The windows only matter once a
			// rule is turned on; speaker.ExperimentalTuning has the values
			// tuned from live meetings.
			StickyGraceWindow:       3.0,
			StickyThresholdMargin:   0,
			MinAssignDuration:       0,
			ShortSegmentGraceWindow: 15.0,

			VideoHintLearnInterval: 1.0,
			VideoHintCheckInterval: 1.0,
		},
	}
}

// Path returns the default config file path (~/.config/tomoe/config.toml).
// Respects $XDG_CONFIG_HOME if set.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tomoe", "config.toml")
}

// ModelDir returns the default model storage directory (~/.local/share/tomoe/models/).
// Respects $XDG_DATA_HOME if set.
func ModelDir() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "tomoe", "models")
}

// SessionDir returns the session storage directory (~/.local/share/tomoe/sessions/).
func SessionDir() string {
	return filepath.Join(DataDir(), "sessions")
}

// HintAnalysisDir is where the hint timeline's "Save for analysis" puts
// looks (~/.local/share/tomoe/hint-analysis/): frames kept for working out
// why the ring or the name wasn't found. Local only.
func HintAnalysisDir() string {
	return filepath.Join(DataDir(), "hint-analysis")
}

// DataDir returns the base data directory (~/.local/share/tomoe/).
func DataDir() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "tomoe")
}

// LibDir returns the directory for additional shared libraries (~/.local/share/tomoe/lib/).
// Used for GPU provider .so files downloaded by `make install-gpu`.
func LibDir() string {
	return filepath.Join(DataDir(), "lib")
}

// Exists reports whether the config file exists at the default path.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Load reads and parses the config file at the given path.
// Starts from DefaultConfig so fields absent from the file retain their defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := DefaultConfig()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return cfg, nil
}

// Watch polls path's mtime every interval and calls onChange with a
// freshly reloaded Config whenever it changes -- the mechanism behind
// retuning live speaker-clustering/video-hint-timing behavior without
// a rebuild or even a relaunch (see MeetingConfig's doc comment).
// onChange is never called concurrently with itself, and never for
// the file's state as of when Watch was called (only for a real
// change made afterward). A reload that fails to parse is logged and
// skipped, leaving whatever's currently applied in place rather than
// falling back to defaults. Returns a stop func; safe to call more
// than once.
func Watch(path string, interval time.Duration, onChange func(*Config)) (stop func()) {
	var lastMod time.Time
	if info, err := os.Stat(path); err == nil {
		lastMod = info.ModTime()
	}

	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				info, err := os.Stat(path)
				if err != nil || !info.ModTime().After(lastMod) {
					continue
				}
				lastMod = info.ModTime()
				cfg, err := Load(path)
				if err != nil {
					fmt.Printf("config: reload of %s failed, keeping previous values: %v\n", path, err)
					continue
				}
				onChange(cfg)
			}
		}
	}()

	return func() { stopOnce.Do(func() { close(done) }) }
}

// Save writes the config to the given path, creating parent directories as needed.
func Save(cfg *Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	header := fmt.Sprintf("# Generated by tomoe auto-init on %s\n\n",
		time.Now().Format(time.RFC3339))

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	content := []byte(header)
	content = append(content, data...)

	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// MeetingLanguages lists the languages a meeting can be recorded in:
// English, plus the chosen languages when multilingual is on.
func (c *Config) MeetingLanguages() []string {
	langs := []string{"en"}
	if c.Multilingual.Enabled {
		for _, l := range c.Multilingual.Languages {
			if !slices.Contains(langs, l) {
				langs = append(langs, l)
			}
		}
	}
	return langs
}
