// Package diarize runs pyannote-style speaker diarization step by step,
// so the expensive steps (segmentation and speaker embeddings) can be
// computed once and cached while the cheap ones (clustering, merging,
// overlap handling) are re-run with different settings in milliseconds.
//
// It follows sherpa-onnx's OfflineSpeakerDiarization (v1.12.28,
// offline-speaker-diarization-pyannote-impl.h) step for step, with the same
// models, so its defaults reproduce what Tomoe's post-meeting pass produces
// today; what it adds is access to the intermediate results and the knobs
// sherpa-onnx keeps internal.
package diarize

import "github.com/sosuke-ai/tomoe-pc/internal/onnxrt"

// initORT loads ONNX Runtime (see onnxrt.Init).
func initORT() error { return onnxrt.Init() }
