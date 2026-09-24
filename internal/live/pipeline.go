package live

import (
	"context"
	"fmt"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

const (
	vadSampleRate = 16000
	vadWindowSize = 512
)

// refinementJob is one pass-2 request: re-decode a completed segment's
// audio through the (offline, higher-quality) Engine and supersede the
// pass-1 text already emitted for it.
type refinementJob struct {
	id        string
	samples   []float32
	speaker   string
	startTime float64
	endTime   float64
	source    SourceType
	pass1Text string // fallback if refinement produces nothing usable
}

// processPipeline runs a single source pipeline: reads windows → VAD → transcribe → emit segments.
func (c *Coordinator) processPipeline(ctx context.Context, sc *audio.StreamCapturer, source SourceType) {
	defer c.wg.Done()

	// Create a VAD instance for this source
	vadConfig := &sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              c.cfg.VADPath,
			Threshold:          0.5,
			MinSilenceDuration: 0.5,
			MinSpeechDuration:  0.25,
			WindowSize:         vadWindowSize,
			MaxSpeechDuration:  30.0,
		},
		SampleRate: vadSampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}

	vad := sherpa.NewVoiceActivityDetector(vadConfig, 60.0)
	if vad == nil {
		return
	}
	defer sherpa.DeleteVoiceActivityDetector(vad)
	sigfix.AfterSherpa()

	// Pass 1 (optional): a streaming session for this source, giving
	// incremental partial text as audio arrives rather than only once a
	// whole VAD segment completes. Nil (falls back to today's
	// single-pass, synchronous-decode-on-completion behavior) if the
	// realtime model isn't configured/available.
	var streamSess transcribe.StreamingSession
	if c.cfg.StreamingEngine != nil {
		var err error
		streamSess, err = c.cfg.StreamingEngine.NewSession()
		if err != nil {
			fmt.Printf("live: failed to start streaming session for %s (falling back to non-realtime): %v\n", source, err)
			streamSess = nil
		} else {
			defer streamSess.Close()
		}
	}
	var partial string

	windows := sc.Windows()
	for {
		select {
		case <-ctx.Done():
			// Flush VAD and process remaining segments
			vad.Flush()
			c.drainVAD(vad, source, streamSess, &partial)
			return

		case window, ok := <-windows:
			if !ok {
				// Channel closed — capturer stopped
				vad.Flush()
				c.drainVAD(vad, source, streamSess, &partial)
				return
			}

			// Feed window to VAD (must be exactly windowSize)
			if len(window) == vadWindowSize {
				vad.AcceptWaveform(window)

				if streamSess != nil {
					text, err := streamSess.Feed(window)
					if err == nil && text != partial {
						partial = text
					}
				}
			}

			// Signal activity when VAD detects ongoing speech
			if vad.IsSpeech() {
				select {
				case c.activityCh <- struct{}{}:
				default:
				}
			}

			// Process any completed speech segments
			c.drainVAD(vad, source, streamSess, &partial)
		}
	}
}

// drainVAD transcribes all completed speech segments from the VAD.
// streamSess/partial are pass 1's streaming state (see processPipeline)
// -- nil/unused when two-pass transcription isn't configured, in which
// case this behaves exactly as before: one synchronous decode per
// completed segment, emitted as final immediately.
func (c *Coordinator) drainVAD(vad *sherpa.VoiceActivityDetector, source SourceType, streamSess transcribe.StreamingSession, partial *string) {
	for !vad.IsEmpty() {
		segment := vad.Front()
		vad.Pop()

		if len(segment.Samples) == 0 {
			continue
		}

		// Apply DSP pipeline
		samples := audio.ProcessPipeline(segment.Samples, vadSampleRate, -40)
		duration := float64(len(samples)) / vadSampleRate
		endTime := c.elapsed()
		startTime := endTime - duration
		spk := c.assignSpeaker(source, samples)

		if streamSess != nil {
			text := strings.TrimSpace(*partial)
			streamSess.Reset()
			*partial = ""

			if text == "" {
				continue
			}

			id := c.nextSegID()
			seg := session.Segment{
				ID:        id,
				Speaker:   spk,
				Text:      text,
				StartTime: startTime,
				EndTime:   endTime,
				Source:    string(source),
				Language:  "en", // the streaming engine is English-only today
				Status:    "pending",
			}
			select {
			case c.segmentCh <- seg:
			default:
			}

			select {
			case c.refineCh <- refinementJob{
				id: id, samples: samples, speaker: spk,
				startTime: startTime, endTime: endTime, source: source,
				pass1Text: text,
			}:
			default:
				// Refinement queue is backed up -- pass 1's text stands
				// as final rather than blocking the live pipeline.
			}
		} else {
			// Single-pass (no streaming engine configured): unchanged
			// from before this feature existed.
			c.transcribeMu.Lock()
			result, err := c.cfg.Engine.TranscribeDirect(samples)
			c.transcribeMu.Unlock()

			if err != nil || result == nil || strings.TrimSpace(result.Text) == "" {
				continue
			}

			seg := session.Segment{
				ID:        c.nextSegID(),
				Speaker:   spk,
				Text:      strings.TrimSpace(result.Text),
				StartTime: startTime,
				EndTime:   endTime,
				Source:    string(source),
				Language:  result.Language,
			}
			select {
			case c.segmentCh <- seg:
			default:
			}
		}

		// Update counters
		if source == SourceMic {
			c.micCount.Add(1)
		} else {
			c.monitorCount.Add(1)
		}
	}
}

// refineWorker drains refinement jobs (pass 2): re-decode a segment's
// audio through the offline Engine (allowed to be slower/better than
// pass 1's streaming decode) and supersede its text via segmentUpdateCh.
// Runs until refineCh is closed (see Start) rather than on ctx.Done(),
// so a job queued right at shutdown still gets processed.
func (c *Coordinator) refineWorker() {
	defer c.refineWG.Done()

	for job := range c.refineCh {
		c.transcribeMu.Lock()
		result, err := c.cfg.Engine.TranscribeDirect(job.samples)
		c.transcribeMu.Unlock()

		text := job.pass1Text
		lang := "en"
		if err == nil && result != nil && strings.TrimSpace(result.Text) != "" {
			text = strings.TrimSpace(result.Text)
			if result.Language != "" {
				lang = result.Language
			}
		}
		// Either way, Status becomes "" (final): refinement failing just
		// means pass 1's text is what stands, not that the segment stays
		// marked "pending" forever.
		seg := session.Segment{
			ID:        job.id,
			Speaker:   job.speaker,
			Text:      text,
			StartTime: job.startTime,
			EndTime:   job.endTime,
			Source:    string(job.source),
			Language:  lang,
		}
		select {
		case c.segmentUpdateCh <- seg:
		default:
		}
	}
}

// assignSpeaker determines the speaker label for a segment.
func (c *Coordinator) assignSpeaker(source SourceType, samples []float32) string {
	if source == SourceMic {
		return "You"
	}

	// For monitor source, try speaker embedding + clustering
	if c.cfg.Embedder != nil && c.cfg.Tracker != nil {
		embedding, err := c.cfg.Embedder.Extract(samples)
		if err == nil && len(embedding) > 0 {
			label, needsHint := c.cfg.Tracker.Assign(embedding)
			if needsHint {
				// Non-blocking: a video-hint check is worth doing right
				// away rather than waiting for videohint.Poll's next
				// scheduled tick, but this pipeline must never stall
				// waiting for a slow/absent consumer.
				select {
				case c.hintNeededCh <- struct{}{}:
				default:
				}
			}
			return label
		}
	}

	return "Other"
}
