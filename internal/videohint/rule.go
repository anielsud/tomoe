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

// Rule holds everything videohint knows about identifying the active
// speaker for one meeting platform. Only Ring exists so far (PR A);
// the name-label region (where to run OCR once a ring is found) is
// added in PR B.
type Rule struct {
	Ring RingConfig
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
// To add a real entry once you have calibration data (e.g. from
// reviewing snapshots under config.UnrecognizedUIDir(), or a live
// session): rules[meeting.PlatformTeams] = Rule{Ring: RingConfig{...}}.
var rules = map[meeting.Platform]Rule{}

// ruleFor returns the rule for platform and whether one exists.
func ruleFor(platform meeting.Platform) (Rule, bool) {
	r, ok := rules[platform]
	return r, ok
}
