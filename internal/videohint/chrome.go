package videohint

import "math"

// ChromeMarker describes a small, fixed-position UI element used to
// confirm a captured window is actually showing an active call, as
// opposed to some other Teams-owned window that a title-based finder
// can still mistake for one — confirmed live: a chat conversation and
// a post-meeting recording/playback page were both captured as "the
// meeting window," and one of them then OCR'd a plausible-looking but
// wrong name straight off unrelated on-screen text (its own window
// title). See docs/macos-support.md.
//
// Matched by hue+saturation rather than exact RGB (DetectCallChrome
// ignores brightness/value when comparing against HueDegrees) on the
// bet that a semantic "danger/leave" accent color keeps its hue across
// light/dark theme even if its brightness shifts — light/dark mode
// mostly changes background/surface luminance, not accent hues, in
// most native UI toolkits. This is a design bet, not a proven fact:
// there's no light-mode capture in this codebase's calibration data to
// verify it against yet (every capture so far is dark mode).
type ChromeMarker struct {
	// SearchX0, SearchX1 bound the horizontal search window as a
	// fixed pixel offset from the frame's *right* edge (e.g. -90 means
	// "90px in from the right edge"). Absolute, not a fraction of
	// frame width: this is native toolbar chrome, and Teams renders it
	// at a constant pixel size/position anchored to the window edge
	// regardless of window size — confirmed empirically: the same
	// offset held across four differently-sized real captures, while
	// a fraction-of-width model would not have (window width isn't
	// what determines toolbar icon position; the fixed margin from
	// the edge is).
	SearchX0, SearchX1 int
	// SearchY0, SearchY1 bound the vertical search window, absolute
	// pixels from the frame's top edge (same reasoning as above).
	SearchY0, SearchY1 int
	// HueDegrees is the target hue (0-360, red is ~0/360).
	HueDegrees float64
	// HueTolerance is the max hue difference to count as a match,
	// handling wraparound around 0/360.
	HueTolerance float64
	// MinSaturation/MinValue reject desaturated or near-black pixels
	// (background chrome, anti-aliased edges) from matching. Both are
	// 0-1.
	MinSaturation, MinValue float64
	// MinPixels/MaxPixels bound the matched pixel count within the
	// search window.
	MinPixels, MaxPixels int
}

// configured reports whether this ChromeMarker has real calibration
// data, as opposed to being the unset zero value.
func (m ChromeMarker) configured() bool {
	return m.MaxPixels > 0
}

// DetectCallChrome reports whether m's marker is present within its
// expected search window — a gate meant to run before ring/label
// detection, so a wrong-kind-of-window capture is rejected outright
// instead of risking a false-positive ring match or a misleading OCR
// read. Returns false immediately if m is unconfigured.
func DetectCallChrome(pix []byte, width, height int, m ChromeMarker) bool {
	if !m.configured() || width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return false
	}

	x0, x1 := clamp(width+m.SearchX0, 0, width), clamp(width+m.SearchX1, 0, width)
	y0, y1 := clamp(m.SearchY0, 0, height), clamp(m.SearchY1, 0, height)
	if x0 >= x1 || y0 >= y1 {
		return false
	}

	count := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := (y*width + x) * 3
			h, s, v := rgbToHSV(pix[i], pix[i+1], pix[i+2])
			if hueDist(h, m.HueDegrees) <= m.HueTolerance && s >= m.MinSaturation && v >= m.MinValue {
				count++
			}
		}
	}
	return count >= m.MinPixels && count <= m.MaxPixels
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// rgbToHSV converts 0-255 RGB to hue (0-360), saturation (0-1), value
// (0-1).
func rgbToHSV(r, g, b uint8) (h, s, v float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	v = max
	d := max - min
	if max > 0 {
		s = d / max
	}
	if d == 0 {
		return 0, s, v
	}
	switch max {
	case rf:
		h = 60 * math.Mod((gf-bf)/d, 6)
	case gf:
		h = 60 * ((bf-rf)/d + 2)
	default:
		h = 60 * ((rf-gf)/d + 4)
	}
	if h < 0 {
		h += 360
	}
	return h, s, v
}

// hueDist is the shortest distance between two hues on the 0-360
// color wheel, handling wraparound (e.g. hueDist(358, 2) == 4, not
// 356).
func hueDist(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}
