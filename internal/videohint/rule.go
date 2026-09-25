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
//
// Deliberately absolute pixels, not a fraction of the ring's own size:
// confirmed against two real captures at very different scales (a
// full-screen 1-on-1 tile, ~1794x1026, and a ~440x245 gallery tile)
// that Teams renders the name label at a fixed on-screen font size
// regardless of tile size. In ring-relative FRACTION terms those two
// examples' labels differed by more than 3x (width fraction 0.11 vs.
// 0.38, height fraction 0.036 vs. 0.10) — but in absolute pixels, the
// offset from the ring's bottom edge (48px vs. 42px) and label height
// (37px vs. 25px) were within a few pixels of each other, exactly what
// fixed-size font rendering predicts and a fraction-of-ring model
// can't express. This is why the original single-tile-scale
// calibration (a fractional bottom-27%-of-ring-height band) worked
// for the tile size it was eyeballed against but not for others.
type LabelRegion struct {
	// BottomOffset is the label's top edge, as a fixed pixel distance
	// above the ring's own bottom edge.
	BottomOffset int
	// Height is the label's fixed pixel height.
	Height int
	// MaxWidth caps the cropped label width in pixels — real names
	// render at varying widths; this just needs to be generous enough
	// to contain them without spilling past the tile into whatever's
	// next to it in a gallery layout. Clamped to the ring's own width
	// if narrower (see LabelRect).
	MaxWidth int
}

// configured reports whether this LabelRegion has real calibration
// data, as opposed to being the unset zero value.
func (l LabelRegion) configured() bool {
	return l.Height > 0
}

// Rule holds everything videohint knows about identifying and naming
// the active speaker for one meeting platform: Chrome (optional) gates
// on whether the captured window is actually showing an active call
// before Ring/Label are even attempted, Ring locates the
// active-speaker border, Label locates that speaker's name text
// relative to it once Ring has matched.
type Rule struct {
	Chrome ChromeMarker
	Ring   RingConfig
	Label  LabelRegion
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
// border, and the participant's name label sits a fixed ~50px above
// the ring's own bottom edge, ~40px tall (see LabelRegion's doc
// comment for why this is absolute pixels rather than a fraction of
// the ring). Re-validated against the escalation snapshot library
// after a first pass wrongly modeled the label as a ring-relative
// fraction (worked for the one tile size it was eyeballed against,
// wrong by 2-4x for others) — measured directly from two real
// escalated frames at very different scales (a full-screen 1-on-1
// tile and a ~440x245 gallery tile) rather than a single eyeballed
// example. These are still single-machine measurements, not a
// stress-tested calibration: ColorTolerance and the area-fraction
// bounds are deliberately generous to survive lighting/monitor
// variation, and the label offset assumes this display's DPI scale
// holds — both may need retuning against more real calls or other
// displays. Two simultaneous rings were observed in one frame
// (multiple recent speakers highlighted at once); DetectRing only
// returns its single best-scoring match today, so a second active
// ring in the same frame is currently missed — a known limitation, not
// addressed by this rule entry.
//
// To add another platform's entry once you have its calibration data
// (e.g. from reviewing snapshots under config.UnrecognizedUIDir(), or
// a live session): rules[meeting.PlatformX] = Rule{Ring: RingConfig{...}, Label: LabelRegion{...}}.
var rules = map[meeting.Platform]Rule{
	meeting.PlatformTeams: {
		// Calibrated against the same escalation snapshot library used
		// for Ring/Label below: the "Leave" hang-up icon's red glyph,
		// found at an exact, repeatable pixel count (110) across every
		// real call frame checked (15/15, spanning four different
		// window sizes), and completely absent (0/7) from every
		// captured window that merely happened to be Teams-owned but
		// wasn't actually a call (a chat conversation, a post-meeting
		// recording/playback page) — both of which FindMeetingWindow's
		// title heuristic alone had mistaken for the meeting window
		// and, in one case, gone on to OCR a plausible-looking but
		// wrong "name" straight off unrelated on-screen text. Bounds
		// are deliberately generous around that clean 110-pixel
		// reading, not tight around it, since MinPixels/MaxPixels only
		// need to reject "clearly absent" (0) and "clearly some other,
		// much bigger red thing," not pin an exact count.
		Chrome: ChromeMarker{
			SearchX0: -100, SearchX1: -5,
			SearchY0: 44, SearchY1: 63,
			HueDegrees: 358, HueTolerance: 20,
			MinSaturation: 0.30, MinValue: 0.20,
			MinPixels: 40, MaxPixels: 250,
		},
		Ring: RingConfig{
			TargetColor:     [3]uint8{129, 136, 243},
			ColorTolerance:  25,
			MinAreaFraction: 0.0001,
			MaxAreaFraction: 0.05,
		},
		Label: LabelRegion{
			BottomOffset: 52,
			Height:       40,
			MaxWidth:     300,
		},
	},
}

// ruleFor returns the rule for platform and whether one exists.
func ruleFor(platform meeting.Platform) (Rule, bool) {
	r, ok := rules[platform]
	return r, ok
}
