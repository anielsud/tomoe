package audio

import "math"

// RemoveDCOffset subtracts the mean from all samples to remove DC bias.
func RemoveDCOffset(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	var sum float64
	for _, s := range samples {
		sum += float64(s)
	}
	mean := float32(sum / float64(len(samples)))

	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = s - mean
	}
	return out
}

// HighPassFilter applies a single-pole IIR high-pass filter.
// Attenuates frequencies below cutoffHz. Typical use: 80Hz to remove
// low-frequency rumble, AC hum, and breath pops.
func HighPassFilter(samples []float32, sampleRate int, cutoffHz float32) []float32 {
	if len(samples) == 0 || sampleRate <= 0 || cutoffHz <= 0 {
		return samples
	}

	// Single-pole IIR: y[n] = alpha * (y[n-1] + x[n] - x[n-1])
	// alpha = RC / (RC + dt), where RC = 1/(2*pi*cutoff), dt = 1/sampleRate
	rc := 1.0 / (2.0 * math.Pi * float64(cutoffHz))
	dt := 1.0 / float64(sampleRate)
	alpha := float32(rc / (rc + dt))

	out := make([]float32, len(samples))
	out[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		out[i] = alpha * (out[i-1] + samples[i] - samples[i-1])
	}
	return out
}

// Normalize scales the signal so the peak amplitude equals 1.0.
// Returns the input unchanged if the signal is silent (all zeros).
func Normalize(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	var peak float32
	for _, s := range samples {
		abs := s
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
	}

	if peak == 0 {
		return samples
	}

	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = s / peak
	}
	return out
}

// LowPassFilter applies a single-pole IIR low-pass filter, complementing
// HighPassFilter above. A single pass only rolls off gently
// (-6dB/octave) — Resample below cascades several passes to get a
// steeper effective cutoff, since it needs this as an anti-aliasing
// filter before decimating, not just gentle tone shaping.
func LowPassFilter(samples []float32, sampleRate int, cutoffHz float32) []float32 {
	if len(samples) == 0 || sampleRate <= 0 || cutoffHz <= 0 {
		return samples
	}

	// Single-pole IIR: y[n] = y[n-1] + alpha * (x[n] - y[n-1])
	// alpha = dt / (RC + dt), where RC = 1/(2*pi*cutoff), dt = 1/sampleRate
	rc := 1.0 / (2.0 * math.Pi * float64(cutoffHz))
	dt := 1.0 / float64(sampleRate)
	alpha := float32(dt / (rc + dt))

	out := make([]float32, len(samples))
	out[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		out[i] = out[i-1] + alpha*(samples[i]-out[i-1])
	}
	return out
}

// NoiseGate zeroes out samples below the given threshold in decibels.
// A typical value is -40 dB. Helps VAD accuracy in noisy environments.
func NoiseGate(samples []float32, thresholdDB float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	// Convert dB threshold to linear amplitude: 10^(dB/20)
	threshold := float32(math.Pow(10, float64(thresholdDB)/20.0))

	out := make([]float32, len(samples))
	for i, s := range samples {
		abs := s
		if abs < 0 {
			abs = -abs
		}
		if abs >= threshold {
			out[i] = s
		}
		// else out[i] remains 0
	}
	return out
}

// antiAliasPasses is how many cascaded LowPassFilter passes Resample
// applies before decimating. One pole only rolls off -6dB/octave, too
// gentle to be a real anti-aliasing filter on its own; cascading a few
// gets a steeper effective cutoff without a more complex filter design.
const antiAliasPasses = 4

// Resample converts samples captured at srcRateHz to dstRateHz using
// linear interpolation, anti-aliased with LowPassFilter when
// downsampling. Used by sources that don't natively capture at
// CaptureSampleRate (e.g. guestaudio's ScreenCaptureKit tap, delivering
// 48kHz) — every downstream consumer (VAD, Parakeet TDT, and critically
// internal/speaker's embedding model) assumes its input already matches
// CaptureSampleRate, since nothing else in this pipeline resamples.
//
// The anti-alias step matters beyond general audio quality: without it,
// frequency content above the new Nyquist limit folds back into the
// audible range as aliasing noise on decimation, which measurably hurts
// speaker-embedding quality — the reason this exists at all is that
// Linux's PulseAudio monitor path captures natively at 16kHz and never
// resamples, so without this, only macOS's speaker clustering would be
// working from degraded input.
//
// Returns samples unchanged if the rates already match or either rate
// is non-positive.
func Resample(samples []float32, srcRateHz, dstRateHz int) []float32 {
	if len(samples) == 0 || srcRateHz <= 0 || dstRateHz <= 0 || srcRateHz == dstRateHz {
		return samples
	}

	if srcRateHz > dstRateHz {
		// Cutoff below the target Nyquist (dstRateHz/2), leaving margin
		// since a single-pole filter's rolloff isn't a brick wall.
		cutoff := float32(dstRateHz) * 0.45
		for i := 0; i < antiAliasPasses; i++ {
			samples = LowPassFilter(samples, srcRateHz, cutoff)
		}
	}

	ratio := float64(srcRateHz) / float64(dstRateHz)
	outLen := int(float64(len(samples)) / ratio)
	if outLen <= 0 {
		return nil
	}

	out := make([]float32, outLen)
	for i := 0; i < outLen; i++ {
		srcPos := float64(i) * ratio
		idx := int(srcPos)
		if idx >= len(samples) {
			idx = len(samples) - 1
		}
		frac := float32(srcPos - float64(idx))

		if idx+1 < len(samples) {
			out[i] = samples[idx] + (samples[idx+1]-samples[idx])*frac
		} else {
			out[i] = samples[idx]
		}
	}
	return out
}

// Resampler is Resample for audio that arrives in chunks, such as
// capture callbacks. Calling Resample on each chunk separately restarts
// the anti-alias filter at every chunk boundary (a click each time) and
// drops each chunk's fractional trailing output sample (the resampled
// stream slowly falls behind real time: ~1ms per second for 1024-sample
// chunks at 48kHz). A Resampler carries both across calls, so chunked
// output matches resampling the whole stream at once. Not safe for
// concurrent use.
type Resampler struct {
	srcRateHz, dstRateHz int
	alpha                float32                  // anti-alias low-pass coefficient; 0 when upsampling
	lp                   [antiAliasPasses]float32 // each cascaded pass's previous output
	started              bool
	pending              []float32 // filtered input not yet fully consumed by interpolation
	pos                  float64   // next output sample's position within pending
}

// NewResampler returns a Resampler converting srcRateHz to dstRateHz.
func NewResampler(srcRateHz, dstRateHz int) *Resampler {
	r := &Resampler{srcRateHz: srcRateHz, dstRateHz: dstRateHz}
	if srcRateHz > dstRateHz && dstRateHz > 0 {
		// Same filter as Resample (see LowPassFilter).
		rc := 1.0 / (2.0 * math.Pi * float64(float32(dstRateHz)*0.45))
		dt := 1.0 / float64(srcRateHz)
		r.alpha = float32(dt / (rc + dt))
	}
	return r
}

// Process resamples the next chunk of the stream. The last input sample
// is held back until the next chunk arrives (it's needed to interpolate
// across the boundary), so output lags input by at most one sample.
func (r *Resampler) Process(samples []float32) []float32 {
	if len(samples) == 0 || r.srcRateHz <= 0 || r.dstRateHz <= 0 || r.srcRateHz == r.dstRateHz {
		return samples
	}

	for _, x := range samples {
		if r.alpha > 0 {
			for p := range r.lp {
				if !r.started {
					r.lp[p] = x
				} else {
					r.lp[p] += r.alpha * (x - r.lp[p])
				}
				x = r.lp[p]
			}
		}
		r.started = true
		r.pending = append(r.pending, x)
	}

	ratio := float64(r.srcRateHz) / float64(r.dstRateHz)
	var out []float32
	for {
		idx := int(r.pos)
		if idx+1 >= len(r.pending) {
			break
		}
		frac := float32(r.pos - float64(idx))
		out = append(out, r.pending[idx]+(r.pending[idx+1]-r.pending[idx])*frac)
		r.pos += ratio
	}

	consumed := min(int(r.pos), len(r.pending))
	r.pending = append(r.pending[:0], r.pending[consumed:]...)
	r.pos -= float64(consumed)
	return out
}

// ProcessPipeline applies all DSP steps in sequence:
// DC offset removal → high-pass filter (80Hz) → normalize → noise gate.
// Set gateDB to 0 to skip the noise gate step.
func ProcessPipeline(samples []float32, sampleRate int, gateDB float32) []float32 {
	result := RemoveDCOffset(samples)
	result = HighPassFilter(result, sampleRate, 80)
	result = Normalize(result)
	if gateDB < 0 {
		result = NoiseGate(result, gateDB)
	}
	return result
}
