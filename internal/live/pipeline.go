package live

import (
	"context"
	"fmt"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

const (
	vadSampleRate = 16000
	vadWindowSize = 512

	// minLiveAudioSamples gates the first live-partial emission (see
	// liveState/emitLivePartial): a speaker assignment needs enough
	// accumulated speech for a stable embedding, so pass 1's partial
	// text is buffered silently until this much speech has accumulated,
	// then shown (and grows from there on every subsequent partial).
	// Half a second is imperceptible as a delay but avoids computing a
	// speaker off a handful of frames.
	minLiveAudioSamples = vadSampleRate / 2
)

// refinementJob is one pass-2 request: re-decode a completed segment's
// audio through the (offline, higher-quality) Engine and supersede the
// pass-1 text already emitted for it.
//
// A segment pass 1 produced no text for (typically a short "yes"/"ok"
// that finished before the streaming model emitted anything) was never
// shown, so it is queued unannounced: refineWorker emits it as a new
// segment if pass 2 finds speech, and drops it otherwise. Its speaker is
// only decided then, from embedding, so noise that decodes to nothing
// never reaches the speaker tracker.
type refinementJob struct {
	id        string
	samples   []float32
	speaker   string
	decision  speaker.AssignDecision // see speakerLabel; "" if speaker is unset
	startTime float64
	endTime   float64
	source    SourceType
	pass1Text string // fallback if refinement produces nothing usable

	unannounced bool
	embedding   []float32 // unannounced only; see speakerEmbedding
}

// liveState tracks pass 1's in-progress utterance for one pipeline: the
// growing partial text, whether (and what) a live segment has already
// been shown for it, and the audio accumulated so far for an early —
// but, once decided, stable — speaker assignment. Zero value is "no
// utterance in progress."
type liveState struct {
	partial   string
	shown     string // last text actually emitted for the live segment
	id        string
	speaker   string
	startTime float64
	audio     []float32
}

func (ls *liveState) reset() {
	*ls = liveState{}
}

// sourceState is one source pipeline's state: its VAD, its optional pass-1
// streaming session, and pass 1's in-progress utterance. Split out of
// processPipeline so Replay can drive the exact same per-window logic
// from recorded audio instead of a live capturer.
type sourceState struct {
	source     SourceType
	vad        *sherpa.VoiceActivityDetector
	streamSess transcribe.StreamingSession
	live       liveState
}

// newSourceState creates the VAD and (if configured) pass-1 streaming
// session for one source. Returns nil if the VAD can't be created.
func (c *Coordinator) newSourceState(source SourceType) *sourceState {
	// Create a VAD instance for this source
	minSilence, maxSpeech := c.cfg.utteranceBounds()
	vadConfig := &sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              c.cfg.VADPath,
			Threshold:          0.5,
			MinSilenceDuration: float32(minSilence),
			MinSpeechDuration:  0.25,
			WindowSize:         vadWindowSize,
			MaxSpeechDuration:  float32(maxSpeech),
		},
		SampleRate: vadSampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}

	vad := sherpa.NewVoiceActivityDetector(vadConfig, 60.0)
	if vad == nil {
		return nil
	}
	sigfix.AfterSherpa()
	st := &sourceState{source: source, vad: vad}

	// Pass 1 (optional): a streaming session for this source, giving
	// incremental partial text as audio arrives rather than only once a
	// whole VAD segment completes. Nil (falls back to today's
	// single-pass, synchronous-decode-on-completion behavior) if the
	// realtime model isn't configured/available.
	if c.cfg.StreamingEngine != nil {
		streamSess, err := c.cfg.StreamingEngine.NewSession()
		if err != nil {
			fmt.Printf("live: failed to start streaming session for %s (falling back to non-realtime): %v\n", source, err)
		} else {
			st.streamSess = streamSess
		}
	}
	return st
}

// close releases the source's VAD and streaming session.
func (st *sourceState) close() {
	if st.streamSess != nil {
		st.streamSess.Close()
	}
	sherpa.DeleteVoiceActivityDetector(st.vad)
}

// processPipeline runs a single source pipeline: reads windows → VAD → transcribe → emit segments.
func (c *Coordinator) processPipeline(ctx context.Context, sc *audio.StreamCapturer, source SourceType) {
	defer c.wg.Done()

	st := c.newSourceState(source)
	if st == nil {
		return
	}
	defer st.close()

	windows := sc.Windows()
	for {
		select {
		case <-ctx.Done():
			c.finishSource(st)
			return

		case window, ok := <-windows:
			if !ok {
				// Channel closed — capturer stopped
				c.finishSource(st)
				return
			}
			c.processWindow(st, window)
		}
	}
}

// finishSource flushes the VAD and processes whatever it still holds,
// once the source has no more audio coming.
func (c *Coordinator) finishSource(st *sourceState) {
	st.vad.Flush()
	c.drainVAD(st.vad, st.source, st.streamSess, &st.live)
	c.finishLive(st.source, &st.live)
}

// processWindow feeds one audio window through VAD (and pass 1, if
// enabled), then handles any speech segments that completed.
func (c *Coordinator) processWindow(st *sourceState, window []float32) {
	// Feed window to VAD (must be exactly windowSize)
	if len(window) == vadWindowSize {
		vadBegan := time.Now()
		st.vad.AcceptWaveform(window)
		c.cfg.Timings.add(&c.timingsOrZero().VAD, vadBegan)
		isSpeech := st.vad.IsSpeech()

		if st.streamSess != nil {
			if isSpeech {
				// Only accumulate speech, not silence -- keeps
				// the eventual speaker embedding clean.
				st.live.audio = append(st.live.audio, window...)
			}
			feedBegan := time.Now()
			text, err := st.streamSess.Feed(window)
			c.cfg.Timings.add(&c.timingsOrZero().Streaming, feedBegan)
			if err == nil && text != st.live.partial {
				st.live.partial = text
				if text != "" {
					c.emitLivePartial(st.source, &st.live, text)
				}
			}
		}

		// Signal activity when VAD detects ongoing speech
		if isSpeech {
			select {
			case c.activityCh <- struct{}{}:
			default:
			}
		}
	}

	// Process any completed speech segments
	c.drainVAD(st.vad, st.source, st.streamSess, &st.live)
}

// emitLivePartial publishes pass 1's growing text for the utterance in
// progress: the first call (once enough audio has accumulated for a
// speaker assignment — see minLiveAudioSamples) creates a new "live"
// segment; every call after that updates the same segment ID in place.
// "live" (not "pending") signals to consumers that this text may still
// change because the person is still talking, not just because pass 2
// hasn't run yet — see handleSegment, which is what actually transitions
// a segment to "pending" once the utterance itself is done.
func (c *Coordinator) emitLivePartial(source SourceType, live *liveState, text string) {
	if live.id == "" {
		if len(live.audio) < minLiveAudioSamples {
			return
		}
		live.id = c.nextSegID()
		live.speaker = c.provisionalSpeaker(source, live.audio)
		live.startTime = c.elapsed()
		live.shown = text

		seg := session.Segment{
			ID:        live.id,
			Speaker:   live.speaker,
			Text:      text,
			StartTime: live.startTime,
			EndTime:   c.elapsed(),
			Source:    string(source),
			Language:  "en", // the streaming engine is English-only today
			Status:    "live",
		}
		select {
		case c.segmentCh <- seg:
		default:
		}
		return
	}

	seg := session.Segment{
		ID:        live.id,
		Speaker:   live.speaker,
		Text:      text,
		StartTime: live.startTime,
		EndTime:   c.elapsed(),
		Source:    string(source),
		Language:  "en",
		Status:    "live",
	}
	live.shown = text
	select {
	case c.segmentUpdateCh <- seg:
	default:
	}
}

// drainVAD transcribes all completed speech segments from the VAD.
// streamSess/live are pass 1's streaming state (see processPipeline)
// -- nil/unused when two-pass transcription isn't configured, in which
// case this behaves exactly as before: one synchronous decode per
// completed segment, emitted as final immediately.
func (c *Coordinator) drainVAD(vad *sherpa.VoiceActivityDetector, source SourceType, streamSess transcribe.StreamingSession, live *liveState) {
	for !vad.IsEmpty() {
		segment := vad.Front()
		vad.Pop()

		if len(segment.Samples) == 0 {
			continue
		}

		// Apply DSP pipeline
		samples := audio.ProcessPipeline(segment.Samples, vadSampleRate, -40)
		c.handleSegment(source, samples, streamSess, live)
	}
}

// handleSegment transcribes one completed VAD segment (already DSP
// processed). Split out of drainVAD so it can be tested without a VAD
// model.
func (c *Coordinator) handleSegment(source SourceType, samples []float32, streamSess transcribe.StreamingSession, live *liveState) {
	duration := float64(len(samples)) / vadSampleRate
	endTime := c.elapsed()
	startTime := endTime - duration

	if streamSess == nil {
		c.transcribeSinglePass(source, samples, startTime, endTime)
		return
	}

	text := strings.TrimSpace(live.partial)
	streamSess.Reset()

	if text == "" && live.id == "" {
		// Pass 1 never produced text for this utterance. Pass 2 still gets
		// a chance at it, the same as single-pass would have.
		c.queueUnannounced(source, samples, startTime, endTime)
		live.reset()
		return
	}
	if text == "" {
		// A live segment is showing but pass 1's hypothesis has since
		// gone blank; keep the utterance and let pass 2 supply the text.
		text = live.shown
	}

	// A live segment keeps its ID, but its speaker label was only
	// provisional (see provisionalSpeaker): the real assignment uses the
	// whole utterance, as single-pass does, and may relabel the line.
	id := live.id
	wasLive := id != ""
	if !wasLive {
		id = c.nextSegID()
	}
	spk, decision := c.assignSpeaker(source, samples, startTime)
	live.reset()

	seg := session.Segment{
		ID:        id,
		Speaker:   spk,
		Text:      text,
		StartTime: startTime,
		EndTime:   endTime,
		Source:    string(source),
		Language:  "en",
		Status:    "pending",
		Decision:  string(decision),
	}
	if wasLive {
		select {
		case c.segmentUpdateCh <- seg:
		default:
		}
	} else {
		select {
		case c.segmentCh <- seg:
		default:
		}
	}
	c.countSegment(source)

	select {
	case c.refineCh <- refinementJob{
		id: id, samples: samples, speaker: spk, decision: decision,
		startTime: startTime, endTime: endTime, source: source,
		pass1Text: text,
	}:
	default:
		// Refinement queue is backed up -- pass 1's text stands as final
		// rather than blocking the live pipeline, and consumers have to be
		// told so or the segment stays "pending" forever.
		seg.Status = ""
		select {
		case c.segmentUpdateCh <- seg:
		default:
		}
	}
}

// queueUnannounced hands a segment pass 1 produced no text for to pass 2
// (see refinementJob). If the refinement queue is backed up it decodes
// synchronously instead: dropping it would lose speech outright, which
// is worse than briefly stalling this pipeline.
func (c *Coordinator) queueUnannounced(source SourceType, samples []float32, startTime, endTime float64) {
	job := refinementJob{
		id: c.nextSegID(), samples: samples,
		startTime: startTime, endTime: endTime, source: source,
		unannounced: true,
		embedding:   c.speakerEmbedding(source, samples),
	}
	select {
	case c.refineCh <- job:
	default:
		if seg, ok := c.refine(job); ok {
			select {
			case c.segmentCh <- seg:
			default:
			}
			c.countSegment(source)
		}
	}
}

// transcribeSinglePass is the single-pass path (no streaming engine
// configured): one synchronous decode per completed segment, emitted as
// final immediately.
func (c *Coordinator) transcribeSinglePass(source SourceType, samples []float32, startTime, endTime float64) {
	began := time.Now()
	defer c.cfg.Timings.utterance(began)
	c.transcribeMu.Lock()
	result, err := c.cfg.Engine.TranscribeDirect(samples)
	c.transcribeMu.Unlock()
	c.cfg.Timings.add(&c.timingsOrZero().Decode, began)

	if err != nil || result == nil || strings.TrimSpace(result.Text) == "" {
		return
	}

	// Only after the text check: a segment that decodes to nothing (noise,
	// a cough) must not create or move a speaker centroid.
	spk, decision := c.assignSpeaker(source, samples, startTime)

	seg := session.Segment{
		ID:        c.nextSegID(),
		Speaker:   spk,
		Text:      strings.TrimSpace(result.Text),
		StartTime: startTime,
		EndTime:   endTime,
		Source:    string(source),
		Language:  result.Language,
		Decision:  string(decision),
		Words:     session.WordsFromTokens(result.Tokens, result.Timestamps, startTime, endTime),
	}
	select {
	case c.segmentCh <- seg:
	default:
	}
	c.countSegment(source)
}

// finishLive runs when a pipeline stops with a live segment still open
// (its utterance never completed as a VAD segment), so that segment gets
// refined text like any other instead of staying "live" in the saved
// session. The refinement queue is still open here -- it is only closed
// once every pipeline has returned -- and blocking on it is fine this
// late, since the pipeline has nothing left to read.
func (c *Coordinator) finishLive(source SourceType, live *liveState) {
	if live.id == "" {
		return
	}
	text := strings.TrimSpace(live.partial)
	if text == "" {
		text = live.shown
	}
	spk, decision := c.assignSpeaker(source, live.audio, live.startTime)
	c.refineCh <- refinementJob{
		id: live.id, samples: live.audio, speaker: spk, decision: decision,
		startTime: live.startTime, endTime: c.elapsed(), source: source,
		pass1Text: text,
	}
	c.countSegment(source)
	live.reset()
}

// countSegment updates the per-source counters reported by Stats.
func (c *Coordinator) countSegment(source SourceType) {
	if source == SourceMic {
		c.micCount.Add(1)
	} else {
		c.monitorCount.Add(1)
	}
}

// refineWorker drains refinement jobs (pass 2): re-decode a segment's
// audio through the offline Engine (allowed to be slower/better than
// pass 1's streaming decode) and supersede its text via segmentUpdateCh,
// or emit it on segmentCh if it was never announced. Runs until refineCh
// is closed (see Start) rather than on ctx.Done(), so a job queued right
// at shutdown still gets processed. Its sends block: this is the final
// text for the segment, and Start's closer goroutine keeps both segment
// channels open until this worker returns.
func (c *Coordinator) refineWorker() {
	defer c.refineWG.Done()

	for job := range c.refineCh {
		seg, ok := c.refine(job)
		if !ok {
			continue
		}
		if job.unannounced {
			c.segmentCh <- seg
			c.countSegment(job.source)
		} else {
			c.segmentUpdateCh <- seg
		}
	}
}

// refine runs pass 2 for one job and returns the final segment. ok is
// false only for an unannounced job pass 2 found no speech in: there is
// nothing to show for it. Otherwise refinement failing just means pass
// 1's text is what stands, not that the segment stays "pending" forever.
func (c *Coordinator) refine(job refinementJob) (seg session.Segment, ok bool) {
	began := time.Now()
	defer c.cfg.Timings.utterance(began)
	c.transcribeMu.Lock()
	result, err := c.cfg.Engine.TranscribeDirect(job.samples)
	c.transcribeMu.Unlock()
	c.cfg.Timings.add(&c.timingsOrZero().Decode, began)

	text := job.pass1Text
	lang := "en"
	var words []session.Word
	if err == nil && result != nil && strings.TrimSpace(result.Text) != "" {
		text = strings.TrimSpace(result.Text)
		if result.Language != "" {
			lang = result.Language
		}
		words = session.WordsFromTokens(result.Tokens, result.Timestamps, job.startTime, job.endTime)
	}
	if text == "" {
		return session.Segment{}, false
	}

	spk, decision := job.speaker, job.decision
	if job.unannounced {
		spk, decision = c.speakerLabel(job.source, job.embedding, sampleDuration(job.samples))
	}
	return session.Segment{
		ID:        job.id,
		Speaker:   spk,
		Text:      text,
		StartTime: job.startTime,
		EndTime:   job.endTime,
		Source:    string(job.source),
		Language:  lang,
		Decision:  string(decision),
		Words:     words,
	}, true
}

// assignSpeaker determines the speaker label for a segment, and which
// rule inside speaker.Tracker.Assign produced it (see speakerLabel).
func (c *Coordinator) assignSpeaker(source SourceType, samples []float32, startTime float64) (string, speaker.AssignDecision) {
	c.probePrefixes(source, samples, startTime)
	c.probeWindows(source, samples, startTime)
	return c.speakerLabel(source, c.speakerEmbedding(source, samples), sampleDuration(samples))
}

// probePrefixes records Config.ProbePrefixes labels for an utterance.
func (c *Coordinator) probePrefixes(source SourceType, samples []float32, startTime float64) {
	if len(c.cfg.ProbePrefixes) == 0 || c.cfg.Probes == nil || c.cfg.Tracker == nil {
		return
	}
	var labels []ProbeLabel
	for _, p := range c.cfg.ProbePrefixes {
		n := int(p * vadSampleRate)
		if n >= len(samples) {
			continue
		}
		if emb := c.speakerEmbedding(source, samples[:n]); len(emb) > 0 {
			labels = append(labels, ProbeLabel{Prefix: p, Speaker: c.cfg.Tracker.Peek(emb)})
		}
	}
	c.cfg.Probes.add(startTime, labels)
}

// probeWindows records Config.WindowSize labels for an utterance.
func (c *Coordinator) probeWindows(source SourceType, samples []float32, startTime float64) {
	size, step := c.cfg.WindowSize, c.cfg.WindowStep
	if size <= 0 || c.cfg.Probes == nil || c.cfg.Tracker == nil {
		return
	}
	if step <= 0 {
		step = size / 2
	}
	n, hop := int(size*vadSampleRate), int(step*vadSampleRate)
	if len(samples) <= n {
		return
	}
	var labels []WindowLabel
	for from := 0; from+n <= len(samples); from += hop {
		emb := c.speakerEmbedding(source, samples[from:from+n])
		if len(emb) == 0 {
			continue
		}
		if label, ok := c.cfg.Tracker.PeekMatch(emb); ok {
			labels = append(labels, WindowLabel{
				Start:   startTime + float64(from)/vadSampleRate,
				End:     startTime + float64(from+n)/vadSampleRate,
				Speaker: label,
			})
		}
	}
	c.cfg.Probes.addWindows(startTime, labels)
}

// sampleDuration is how long samples (at vadSampleRate) plays for.
func sampleDuration(samples []float32) time.Duration {
	return time.Duration(float64(len(samples)) / vadSampleRate * float64(time.Second))
}

// provisionalSpeaker labels a live segment from its first fraction of a
// second of audio without touching the tracker's clusters (see
// speaker.Tracker.Peek); handleSegment assigns the real label once the
// utterance is complete.
func (c *Coordinator) provisionalSpeaker(source SourceType, samples []float32) string {
	if source == SourceMic {
		return "You"
	}
	if c.cfg.SkipMonitorDiarization {
		return "System Audio"
	}
	if embedding := c.speakerEmbedding(source, samples); len(embedding) > 0 {
		return c.cfg.Tracker.Peek(embedding)
	}
	return "Other"
}

// speakerEmbedding extracts the embedding speakerLabel needs, or nil for
// sources labeled without one. Split from speakerLabel so the embedding
// can be computed on the pipeline goroutine (the Embedder is not safe for
// concurrent use) while the label is decided later by refineWorker.
func (c *Coordinator) speakerEmbedding(source SourceType, samples []float32) []float32 {
	if source == SourceMic || c.cfg.SkipMonitorDiarization || c.cfg.Embedder == nil || c.cfg.Tracker == nil {
		return nil
	}
	embedBegan := time.Now()
	embedding, err := c.cfg.Embedder.Extract(samples)
	c.cfg.Timings.add(&c.timingsOrZero().Embed, embedBegan)
	if err != nil {
		return nil
	}
	return embedding
}

// speakerLabel maps a segment's embedding (from speakerEmbedding) to a
// speaker label, clustering monitor-source speakers via the Tracker, and
// which rule inside Tracker.Assign produced it ("" when Assign was never
// called, e.g. "You"/"System Audio"/"Other") -- see speaker.AssignDecision
// and session.Segment.Decision, which a diagnostics view uses to show how
// each line was labeled. duration is how much audio the embedding was
// computed from (see minAssignDuration's doc comment in internal/speaker).
func (c *Coordinator) speakerLabel(source SourceType, embedding []float32, duration time.Duration) (string, speaker.AssignDecision) {
	if source == SourceMic {
		return "You", ""
	}

	if c.cfg.SkipMonitorDiarization {
		return "System Audio", ""
	}

	// For monitor source, try speaker embedding + clustering
	if len(embedding) > 0 && c.cfg.Tracker != nil {
		before := c.cfg.Tracker.NumSpeakers()
		assignBegan := time.Now()
		label, needsHint := c.cfg.Tracker.Assign(embedding, duration)
		c.cfg.Timings.add(&c.timingsOrZero().Assign, assignBegan)
		decision := c.cfg.Tracker.LastDecision()
		isNew := c.cfg.Tracker.NumSpeakers() > before

		if isNew {
			// A brand-new speaker is rarer and more valuable to
			// resolve than an ordinary "still no hint" retry --
			// found live: waiting on the regular debounced trigger
			// alone made naming attempts feel too infrequent to
			// ever catch a fast-moving ring. Fire twice,
			// bypassing the usual debounce entirely: once right
			// now, and once again ~500ms later in case the ring/
			// label hadn't rendered yet on the first attempt.
			c.signalHintNeeded(true)
			go func() {
				time.Sleep(500 * time.Millisecond)
				c.signalHintNeeded(true)
			}()
		} else if needsHint {
			// Non-blocking: a video-hint check is worth doing right
			// away rather than waiting for videohint.Poll's next
			// scheduled tick, but this pipeline must never stall
			// waiting for a slow/absent consumer.
			c.signalHintNeeded(false)
		}
		return label, decision
	}

	return "Other", ""
}
