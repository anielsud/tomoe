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

// Resample converts samples captured at srcRateHz to dstRateHz using
// linear interpolation. Used by sources that don't natively capture at
// CaptureSampleRate (e.g. guestaudio's ScreenCaptureKit tap, delivering
// 48kHz) — every downstream consumer (VAD, Parakeet TDT) assumes its
// input already matches CaptureSampleRate, since nothing else in this
// pipeline resamples. Returns samples unchanged if the rates already
// match or either rate is non-positive.
func Resample(samples []float32, srcRateHz, dstRateHz int) []float32 {
	if len(samples) == 0 || srcRateHz <= 0 || dstRateHz <= 0 || srcRateHz == dstRateHz {
		return samples
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
