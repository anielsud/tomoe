package live

import (
	"context"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
)

const (
	vadSampleRate = 16000
	vadWindowSize = 512
)

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

	windows := sc.Windows()
	for {
		select {
		case <-ctx.Done():
			// Flush VAD and process remaining segments
			vad.Flush()
			c.drainVAD(vad, source)
			return

		case window, ok := <-windows:
			if !ok {
				// Channel closed — capturer stopped
				vad.Flush()
				c.drainVAD(vad, source)
				return
			}

			// Feed window to VAD (must be exactly windowSize)
			if len(window) == vadWindowSize {
				vad.AcceptWaveform(window)
			}

			// Signal activity when VAD detects ongoing speech
			if vad.IsSpeech() {
				select {
				case c.activityCh <- struct{}{}:
				default:
				}
			}

			// Process any completed speech segments
			c.drainVAD(vad, source)
		}
	}
}

// drainVAD transcribes all completed speech segments from the VAD.
func (c *Coordinator) drainVAD(vad *sherpa.VoiceActivityDetector, source SourceType) {
	for !vad.IsEmpty() {
		segment := vad.Front()
		vad.Pop()

		if len(segment.Samples) == 0 {
			continue
		}

		// Apply DSP pipeline
		samples := audio.ProcessPipeline(segment.Samples, vadSampleRate, -40)

		// Transcribe directly — audio is already VAD-segmented
		c.transcribeMu.Lock()
		result, err := c.cfg.Engine.TranscribeDirect(samples)
		c.transcribeMu.Unlock()

		if err != nil || result == nil || strings.TrimSpace(result.Text) == "" {
			continue
		}

		endTime := c.elapsed()
		startTime := endTime - result.Duration

		// Determine speaker label
		speaker := c.assignSpeaker(source, samples)

		seg := session.Segment{
			ID:        c.nextSegID(),
			Speaker:   speaker,
			Text:      strings.TrimSpace(result.Text),
			StartTime: startTime,
			EndTime:   endTime,
			Source:    string(source),
			Language:  result.Language,
		}

		// Emit segment (non-blocking)
		select {
		case c.segmentCh <- seg:
		default:
			// Drop if consumer is too slow
		}

		// Update counters
		if source == SourceMic {
			c.micCount.Add(1)
		} else {
			c.monitorCount.Add(1)
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
