package live

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// SourceType identifies an audio source.
type SourceType string

const (
	SourceMic     SourceType = "mic"
	SourceMonitor SourceType = "monitor"
)

// Config configures the live transcription coordinator.
type Config struct {
	// MicCapturer is the microphone stream capturer (optional, nil to skip mic).
	MicCapturer *audio.StreamCapturer
	// MonitorCapturer is the monitor stream capturer (optional, nil to skip monitor).
	MonitorCapturer *audio.StreamCapturer
	// Engine is the transcription engine (required).
	Engine transcribe.Engine
	// Embedder is the speaker embedding extractor (optional, nil to skip speaker ID for monitor).
	Embedder *speaker.Embedder
	// Tracker is the speaker clustering tracker (optional, nil to skip speaker ID for monitor).
	Tracker *speaker.Tracker
	// VADPath is the path to the Silero VAD model file.
	VADPath string
	// SegmentBufferSize is the channel buffer for output segments.
	SegmentBufferSize int
	// StreamingEngine enables two-pass transcription (optional, nil to
	// keep today's single-pass behavior): pass 1 is StreamingEngine,
	// decoded incrementally so text appears as it's spoken rather than
	// only once a whole VAD segment completes; pass 2 re-decodes the
	// same completed segment through Engine (offline, allowed to be
	// slower/better) and supersedes pass 1's text once ready. See
	// pipeline.go's drainVAD.
	StreamingEngine transcribe.StreamingEngine
	// SkipMonitorDiarization disables speaker embedding/clustering for
	// the monitor source, labeling every monitor-source segment
	// "System Audio" instead of attempting "Person N" identification.
	// Set this when the monitor source is a whole-system audio tap
	// (macOS's "Everything" source-picker option) rather than one
	// specific app's audio: a system-wide tap can mix multiple
	// unrelated audio streams together (notifications, music, several
	// apps at once), so per-embedding speaker clustering isn't
	// meaningful there the way it is for one app's own audio. Defaults
	// to false (diarize). The app no longer sets it: "Everything" is
	// diarized too, since in a meeting it's mostly the call (and a
	// notification sound is just one more short "speaker").
	SkipMonitorDiarization bool
	// Timings, if set, collects per-stage processing time (see Timings).
	Timings *Timings

	// MinSilenceDuration is the pause (seconds) that ends an utterance,
	// and MaxSpeechDuration the longest utterance before it's cut. Each
	// utterance gets one speaker embedding, so these set how much audio
	// a speaker label is based on. 0 means the defaults, 0.5 and 30.
	MinSilenceDuration float64
	MaxSpeechDuration  float64
	// MinSpeechLevelDB drops an utterance whose audio is quieter than this
	// (RMS, dBFS) before it's transcribed: the speech detector triggers
	// on room noise near the noise floor and the model then invents words
	// for it ("Yeah.", "Goodbye"). 0 keeps every utterance.
	MinSpeechLevelDB float64
	// MicLevelMarginDB drops mic utterances more than this many dB below
	// the mic's typical (median) level: the user's own voice is loud on
	// their microphone; faint speech is background. 0 turns it off.
	MicLevelMarginDB float64

	// TurnMode (experimental, single-pass only) decodes a speaker's turn
	// once instead of each utterance: consecutive utterances from one
	// source collect into one line until the other source speaks, a pause
	// longer than TurnMaxGap, the line would pass TurnMaxSeconds, or (call
	// audio) SpeakerChanged reports a change between them. Longer lines
	// give the model more context; cutting at speaker changes keeps one
	// speaker per line.
	TurnMode       bool
	TurnMaxSeconds float64
	TurnMaxGap     float64
	// DecodePad is seconds of silence added before and after the audio of
	// every decode: models that decode a whole utterance at once can drop
	// a last syllable that ends abruptly. 0 adds none.
	DecodePad float64
	// SpeakerChanged reports whether a speaker-change signal (a new voice
	// starting, the meeting window's highlight moving) fell between from
	// and to (session seconds). Nil: no signal beyond the rules above.
	SpeakerChanged func(from, to float64) bool

	// ProbePrefixes, for measurement only, also labels each utterance
	// from just its first N seconds for every N listed that's shorter
	// than the utterance, read-only (speaker.Tracker.Peek) just before
	// the real assignment, recording them in Probes. Costs one embedding
	// per prefix.
	ProbePrefixes []float64
	Probes        *Probes

	// ReplayProgress, if set, is called by Replay every few seconds of
	// audio with the analysis windows done and the total, for tools that
	// report progress. Ignored when capturing live.
	ReplayProgress func(done, total int)

	// WindowSize and WindowStep (seconds), for measurement only, also
	// match overlapping windows within each utterance longer than
	// WindowSize against the known speakers (speaker.Tracker.PeekMatch),
	// recording them in Probes: whether labeling within an utterance
	// catches speaker changes that have no pause. 0 = off.
	WindowSize, WindowStep float64

	// MonitorAudio, if set, receives all of the monitor source's audio,
	// speech or not, one capture window at a time, with the session time
	// (seconds) at the window's end: what diarizing during the meeting
	// runs on (see diarize.Stream). Called on the pipeline goroutine, so
	// it must return quickly.
	MonitorAudio func(samples []float32, endTime float64)

	// OnMonitorSpeechStart, if set, is called when the monitor source's
	// speech starts after at least speechStartPause of quiet: the
	// earliest sign of a speaker change. On the pipeline goroutine, so it
	// must return quickly.
	OnMonitorSpeechStart func()
}

// speechStartPause is the quiet before speech that counts as a likely
// speaker change (see Config.OnMonitorSpeechStart).
const speechStartPause = 0.25

// Default utterance boundaries (see Config.MinSilenceDuration).
const (
	DefaultMinSilenceDuration = 0.5
	DefaultMaxSpeechDuration  = 30.0
)

// utteranceBounds is cfg's MinSilenceDuration and MaxSpeechDuration, with
// defaults for unset values.
func (cfg Config) utteranceBounds() (minSilence, maxSpeech float64) {
	minSilence, maxSpeech = cfg.MinSilenceDuration, cfg.MaxSpeechDuration
	if minSilence <= 0 {
		minSilence = DefaultMinSilenceDuration
	}
	if maxSpeech <= 0 {
		maxSpeech = DefaultMaxSpeechDuration
	}
	return minSilence, maxSpeech
}

// Stats holds runtime statistics about the coordinator.
type Stats struct {
	MicSegments     int
	MonitorSegments int
	Duration        time.Duration
}

// Coordinator manages one or two live transcription pipelines (mic + monitor).
type Coordinator struct {
	levelMu    sync.Mutex
	micLevels  []float64 // recent mic utterance levels (see tooQuiet)
	turnMu     sync.Mutex
	turns      map[SourceType]*turnBuf // TurnMode's open line per source
	turnCuts   map[string]int          // why TurnMode sent each line (see TurnCuts)
	cfg        Config
	segmentCh  chan session.Segment
	activityCh chan struct{} // signalled when VAD detects ongoing speech
	// hintNeededCh is signalled when a monitor-source speaker with no
	// video hint yet is heard. The bool is a priority flag: true means
	// "bypass videohint.Poll's normal trigger debounce" (used for a
	// brand-new speaker, where a fast double-shot attempt matters more
	// than the usual rate limit), false is the ordinary debounced case.
	hintNeededCh chan bool
	startTime    time.Time

	// micHeardAt is when the mic last delivered any real signal (Unix
	// nanoseconds; 0 = not yet), for MicQuietFor.
	micHeardAt atomic.Int64

	// segmentUpdateCh carries revisions to a segment already sent on
	// segmentCh (same ID) -- pass 2's refined text superseding pass 1's.
	// Only used when cfg.StreamingEngine is set.
	segmentUpdateCh chan session.Segment
	// refineCh queues pass-2 refinement jobs from drainVAD to
	// refineWorker. Deliberately drained to completion on shutdown (see
	// Start's closer goroutine) rather than abandoned on ctx.Done(), so
	// a segment queued for refinement right as the session stops still
	// gets its update rather than staying "pending" forever.
	refineCh chan refinementJob
	refineWG sync.WaitGroup

	cancel context.CancelFunc
	wg     sync.WaitGroup

	micCount     atomic.Int64
	monitorCount atomic.Int64

	// transcribeMu serializes transcription calls (sherpa-onnx thread safety).
	transcribeMu sync.Mutex

	// segIDCounter generates unique segment IDs.
	segIDCounter atomic.Int64

	// now is time.Now, except under Replay, which substitutes a virtual
	// clock so recorded audio can run faster than real time with the
	// same segment timestamps.
	now func() time.Time
}

// New creates a new Coordinator with the given configuration.
func New(cfg Config) *Coordinator {
	bufSize := cfg.SegmentBufferSize
	if bufSize <= 0 {
		bufSize = 64
	}
	return &Coordinator{
		cfg:             cfg,
		segmentCh:       make(chan session.Segment, bufSize),
		segmentUpdateCh: make(chan session.Segment, bufSize),
		activityCh:      make(chan struct{}, 1),
		hintNeededCh:    make(chan bool, 1),
		refineCh:        make(chan refinementJob, bufSize),
		now:             time.Now,
	}
}

// Start begins processing audio from all configured sources.
func (c *Coordinator) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	c.startTime = c.now()

	hasMic := c.cfg.MicCapturer != nil
	hasMonitor := c.cfg.MonitorCapturer != nil

	if !hasMic && !hasMonitor {
		return fmt.Errorf("at least one audio source is required")
	}

	if hasMic {
		if err := c.cfg.MicCapturer.Start(); err != nil {
			return fmt.Errorf("starting mic capturer: %w", err)
		}
		c.wg.Add(1)
		go c.processPipeline(ctx, c.cfg.MicCapturer, SourceMic)
	}

	if hasMonitor {
		if err := c.cfg.MonitorCapturer.Start(); err != nil {
			if hasMic {
				_ = c.cfg.MicCapturer.Stop()
			}
			return fmt.Errorf("starting monitor capturer: %w", err)
		}
		c.wg.Add(1)
		go c.processPipeline(ctx, c.cfg.MonitorCapturer, SourceMonitor)
	}

	c.refineWG.Add(1)
	go c.refineWorker()

	// Closer goroutine: waits for all pipelines to finish (including
	// their final VAD flush, which may still enqueue a last refinement
	// job or two), only THEN closes refineCh -- so refineWorker keeps
	// draining right up until every queued job has actually run, never
	// abandoning a "pending" segment mid-refinement -- and only after
	// refineWorker itself has finished does it close the two segment
	// channels, since a late refinement update would otherwise arrive
	// after SegmentUpdates() looks closed to a consumer.
	go func() {
		c.wg.Wait()
		close(c.refineCh)
		c.refineWG.Wait()
		close(c.segmentCh)
		close(c.segmentUpdateCh)
	}()

	return nil
}

// Segments returns the channel that receives newly-transcribed segments.
func (c *Coordinator) Segments() <-chan session.Segment {
	return c.segmentCh
}

// SegmentUpdates returns the channel that receives revisions to a
// segment already delivered on Segments() (same ID): pass 2's refined
// text superseding pass 1's, once ready. Only fires when the Config
// this Coordinator was created with set StreamingEngine.
func (c *Coordinator) SegmentUpdates() <-chan session.Segment {
	return c.segmentUpdateCh
}

// Activity returns a channel signalled when VAD detects ongoing speech.
// Use this to reset silence timers — speech is in progress even though
// no completed segment has been emitted yet.
func (c *Coordinator) Activity() <-chan struct{} {
	return c.activityCh
}

// HintNeeded returns a channel signalled whenever a monitor-source
// speaker with no video hint attached yet is heard. Wire this into
// videohint.Poll's trigger parameter so a still-unlabeled speaker gets
// an immediate ring/OCR attempt instead of waiting for Poll's next
// scheduled tick — see internal/speaker.Tracker.Assign's needsHint
// return value, which is what actually decides when this fires. The
// bool is a priority flag (true = bypass Poll's normal trigger
// debounce) -- see signalHintNeeded.
func (c *Coordinator) HintNeeded() <-chan bool {
	return c.hintNeededCh
}

// signalHintNeeded sends on hintNeededCh without blocking -- if the
// single-slot buffer is already full (Poll hasn't drained the
// previous signal yet), this drops rather than stalling the
// transcription pipeline, same as every other non-blocking channel
// send in this package.
func (c *Coordinator) signalHintNeeded(priority bool) {
	select {
	case c.hintNeededCh <- priority:
	default:
	}
}

// Stop stops all pipelines and waits for them to finish.
// Closes the underlying audio capturers to release device handles.
func (c *Coordinator) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.cfg.MicCapturer != nil {
		_ = c.cfg.MicCapturer.Stop()
	}
	if c.cfg.MonitorCapturer != nil {
		_ = c.cfg.MonitorCapturer.Stop()
	}
	c.wg.Wait()

	// Close capturers to release underlying audio devices.
	// Must happen after wg.Wait() so pipelines are done reading.
	if c.cfg.MicCapturer != nil {
		c.cfg.MicCapturer.Close()
	}
	if c.cfg.MonitorCapturer != nil {
		c.cfg.MonitorCapturer.Close()
	}
}

// Stats returns runtime statistics.
func (c *Coordinator) Stats() Stats {
	return Stats{
		MicSegments:     int(c.micCount.Load()),
		MonitorSegments: int(c.monitorCount.Load()),
		Duration:        c.now().Sub(c.startTime),
	}
}

// AudioSamples returns accumulated audio from all sources (concatenated).
// For single-source sessions only. Use MicSamples/MonitorSamples for per-track access.
func (c *Coordinator) AudioSamples() []float32 {
	var all []float32
	if c.cfg.MicCapturer != nil {
		all = append(all, c.cfg.MicCapturer.AllSamples()...)
	}
	if c.cfg.MonitorCapturer != nil {
		all = append(all, c.cfg.MonitorCapturer.AllSamples()...)
	}
	return all
}

// MicSamples returns accumulated audio from the mic source, or nil if no mic.
func (c *Coordinator) MicSamples() []float32 {
	if c.cfg.MicCapturer == nil {
		return nil
	}
	return c.cfg.MicCapturer.AllSamples()
}

// MonitorSamples returns accumulated audio from the monitor source, or nil if no monitor.
func (c *Coordinator) MonitorSamples() []float32 {
	if c.cfg.MonitorCapturer == nil {
		return nil
	}
	return c.cfg.MonitorCapturer.AllSamples()
}

// IsDualSource returns true if both mic and monitor sources are configured.
func (c *Coordinator) IsDualSource() bool {
	return c.cfg.MicCapturer != nil && c.cfg.MonitorCapturer != nil
}

func (c *Coordinator) nextSegID() string {
	id := c.segIDCounter.Add(1)
	return fmt.Sprintf("seg-%d", id)
}

func (c *Coordinator) elapsed() float64 {
	return c.now().Sub(c.startTime).Seconds()
}

// MicQuietFor is how long the mic has delivered essentially nothing
// (below about -80 dBFS: a disabled or wrong device, not a quiet room),
// counted from the start if it never has; 0 without a mic.
func (c *Coordinator) MicQuietFor() time.Duration {
	if c.cfg.MicCapturer == nil {
		return 0
	}
	at := c.micHeardAt.Load()
	if at == 0 {
		return c.now().Sub(c.startTime)
	}
	return c.now().Sub(time.Unix(0, at))
}

// SessionTime converts a wall-clock time to session time (seconds since
// the session started), the time base segments use.
func (c *Coordinator) SessionTime(t time.Time) float64 {
	return t.Sub(c.startTime).Seconds()
}
