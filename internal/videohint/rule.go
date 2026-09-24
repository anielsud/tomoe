// Package videohint reads a meeting app's on-screen active-speaker UI
// (a colored ring/border around whichever tile is talking, plus that
// tile's name label) and turns it into a naming hint for
// internal/speaker's audio-only clustering — the "label cluster ID with
// name if a fresh hint is available" step described in
// docs/macos-support.md's architecture section, which that doc already
// flags as "the only new logic; it hasn't been built yet."
//
// This package ships in three stages, each its own PR:
//   - Rule table + ring detection + an escalation snapshot library for
//     UIs the rule table doesn't recognize yet (this file and
//     ring.go/snapshot.go/poller_*.go).
//   - Vision.framework OCR for the name label itself (a ring alone has
//     no name to attach).
//   - Wiring a successful ring+OCR result into actually relabeling an
//     internal/speaker.Tracker cluster during a live session.
package videohint

import "github.com/sosuke-ai/tomoe-pc/internal/meeting"

// RingConfig describes what a meeting app's active-speaker ring looks
// like: its color, how close a pixel must be to count as a match, and
// the ring's expected size relative to the whole captured frame (to
// reject both single-pixel noise and implausibly large "rings").
// The zero value means "unconfigured" — DetectRing returns no match
// immediately for it, without doing any pixel work.
type RingConfig struct {
	// TargetColor is the ring's color as 0-255 RGB.
	TargetColor [3]uint8
	// ColorTolerance is the maximum per-channel absolute difference
	// from TargetColor for a pixel to count as part of the ring.
	ColorTolerance uint8
	// MinAreaFraction and MaxAreaFraction bound a candidate region's
	// pixel count as a fraction of the total frame area (width*height),
	// filtering out noise (too small) and implausible whole-frame
	// matches (too large).
	MinAreaFraction float64
	MaxAreaFraction float64
}

// configured reports whether this RingConfig has real calibration
// data, as opposed to being the unset zero value.
func (c RingConfig) configured() bool {
	return c.ColorTolerance > 0 && c.MaxAreaFraction > 0
}

// LabelRegion describes where a meeting app draws its active-speaker's
// name label, relative to the ring's own bounding box (Teams overlays
// the label inside the bottom edge of the ring, not below it — that's
// an empirical finding, not an assumption; see Rule's Teams entry).
// Both fractions are relative to the ring's Height; the label spans
// the ring's full Width.
type LabelRegion struct {
	// YFraction is the label's top edge, as a fraction of ring height,
	// measured down from the ring's own top edge.
	YFraction float64
	// HeightFraction is the label's height, as a fraction of ring
	// height.
	HeightFraction float64
}

// configured reports whether this LabelRegion has real calibration
// data, as opposed to being the unset zero value.
func (l LabelRegion) configured() bool {
	return l.HeightFraction > 0
}

// Rule holds everything videohint knows about identifying and naming
// the active speaker for one meeting platform: Ring locates the
// active-speaker border, Label locates that speaker's name text
// relative to it once Ring has matched.
type Rule struct {
	Ring  RingConfig
	Label LabelRegion
}

// rules is the platform rule table. It starts intentionally EMPTY, not
// with placeholder/guessed values: verifying real ring color/shape
// thresholds requires either live access to an active multi-participant
// call in each app, or reviewing captured examples from the escalation
// snapshot library (CaptureSnapshot/config.UnrecognizedUIDir) once it
// has real data in it. Guessing thresholds now would risk confidently
// mislabeling a real meeting from an unverified rule, which is worse
// than the honest "Person N" fallback every cluster without a hint
// already gets.
//
// PlatformTeams is the first (and so far only) real entry, calibrated
// against a live, multi-participant Teams call on 2026-09-24 (with
// explicit authorization to capture from that call for this purpose):
// the active-speaker ring is an RGB(129,136,243) hollow rounded-square
// border, and the participant's name label overlays the bottom ~27%
// of the ring's own bounding box (not a separate region below it —
// confirmed by cropping and visually inspecting real frames). These
// are single-session measurements, not a stress-tested calibration:
// ColorTolerance and the area-fraction bounds are deliberately generous
// to survive lighting/monitor variation, and both may need retuning
// against more real calls. Two simultaneous rings were observed in one
// frame (multiple recent speakers highlighted at once); DetectRing
// only returns its single best-scoring match today, so a second active
// ring in the same frame is currently missed — a known limitation, not
// addressed by this rule entry.
//
// To add another platform's entry once you have its calibration data
// (e.g. from reviewing snapshots under config.UnrecognizedUIDir(), or
// a live session): rules[meeting.PlatformX] = Rule{Ring: RingConfig{...}, Label: LabelRegion{...}}.
var rules = map[meeting.Platform]Rule{
	meeting.PlatformTeams: {
		Ring: RingConfig{
			TargetColor:     [3]uint8{129, 136, 243},
			ColorTolerance:  25,
			MinAreaFraction: 0.0001,
			MaxAreaFraction: 0.05,
		},
		Label: LabelRegion{
			YFraction:      0.73,
			HeightFraction: 0.27,
		},
	},
}

// ruleFor returns the rule for platform and whether one exists.
func ruleFor(platform meeting.Platform) (Rule, bool) {
	r, ok := rules[platform]
	return r, ok
}
