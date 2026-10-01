package videohint

import "sort"

// RingMatch describes one detected ring/border candidate's bounding box
// within the frame it was found in.
type RingMatch struct {
	X, Y, Width, Height int
	// Confidence is this candidate's "hollowness" (1 - filled fraction
	// of its bounding box) — closer to 1 means a thinner, more
	// ring-shaped border; closer to 0 means a more solid blob.
	Confidence float64
}

// hollownessFloor rejects candidates that are too filled-in to be a
// thin ring/border (a solid rectangle would score near 0 here).
const hollownessFloor = 0.3

// DetectRing looks for a hollow rectangular border matching cfg within
// an RGB pixel buffer (packed, no padding, 3 bytes per pixel — the same
// shape as teamsvideo.Frame.Pix). Returns the single plausible
// candidate, or (nil, false, false) if cfg is unconfigured (the zero
// value — see RingConfig) or nothing plausible is found.
//
// ambiguous is true when MORE THAN ONE plausible candidate was found
// in the same frame (match is nil in that case too) — observed live:
// Teams can highlight more than one recent speaker at once (e.g. two
// people who both just finished talking), and picking "the best-
// scoring one" risked confidently attributing a naming hint to the
// WRONG person. Skipping attribution for an ambiguous frame is safer
// than guessing; the caller gets another chance on the next poll once
// only one ring remains lit.
//
// Algorithm: color-threshold every pixel against cfg.TargetColor within
// cfg.ColorTolerance, connected-components label the resulting mask
// (4-connectivity flood fill), then for each component check it's
// plausibly ring-shaped (its pixel count is well below its bounding
// box's full area — a filled blob wouldn't be) and within
// cfg.MinAreaFraction/MaxAreaFraction of the whole frame.
func DetectRing(pix []byte, width, height int, cfg RingConfig) (match *RingMatch, found bool, ambiguous bool) {
	candidates := ringCandidates(pix, width, height, cfg)
	switch len(candidates) {
	case 0:
		return nil, false, false
	case 1:
		return &candidates[0], true, false
	default:
		return nil, false, true
	}
}

// RingStat is one ring-colored region's shape measurements, recorded
// with each look so the detection thresholds can be tuned offline: its
// box, its share of the frame, hollowness, border share and edge cover
// (see borderShape), and whether it passed as a ring.
type RingStat struct {
	Box    RingMatch `json:"box"`
	Area   float64   `json:"area"`
	Hollow float64   `json:"hollow"`
	Border float64   `json:"border"`
	Cover  float64   `json:"cover"`
	Ring   bool      `json:"ring"`
}

// maxRingStats caps how many regions a look records (largest first).
const maxRingStats = 20

// ringCandidates finds every component that passes DetectRing's tests.
func ringCandidates(pix []byte, width, height int, cfg RingConfig) []RingMatch {
	rings, _ := ringCandidatesStats(pix, width, height, cfg)
	return rings
}

// DetectRingsWithStats is DetectRings plus the shape measurements of every
// region near the thresholds (half the minimum area up to twice the
// maximum, at least somewhat hollow), rings included.
func DetectRingsWithStats(pix []byte, width, height int, cfg RingConfig) ([]RingMatch, []RingStat) {
	return ringCandidatesStats(pix, width, height, cfg)
}

func ringCandidatesStats(pix []byte, width, height int, cfg RingConfig) ([]RingMatch, []RingStat) {
	if !cfg.configured() || width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return nil, nil
	}

	mask := make([]bool, width*height)
	for i := 0; i < width*height; i++ {
		r, g, b := pix[i*3], pix[i*3+1], pix[i*3+2]
		if absDiff(r, cfg.TargetColor[0]) <= cfg.ColorTolerance &&
			absDiff(g, cfg.TargetColor[1]) <= cfg.ColorTolerance &&
			absDiff(b, cfg.TargetColor[2]) <= cfg.ColorTolerance {
			mask[i] = true
		}
	}

	labels, numComponents := connectedComponents(mask, width, height)
	if numComponents == 0 {
		return nil, nil
	}

	frameArea := float64(width * height)
	var candidates []RingMatch
	var stats []RingStat

	for ci, st := range componentStats(labels, width, numComponents) {
		minX, minY, maxX, maxY, count := st.minX, st.minY, st.maxX, st.maxY, st.count
		if count == 0 {
			continue
		}

		areaFrac := float64(count) / frameArea
		if areaFrac < cfg.MinAreaFraction/2 || areaFrac > cfg.MaxAreaFraction*2 {
			continue
		}
		bw, bh := maxX-minX+1, maxY-minY+1
		fullArea := float64(bw * bh)
		hollowness := 1.0 - float64(count)/fullArea
		if hollowness < hollownessFloor/2 {
			continue
		}
		box := RingMatch{X: minX, Y: minY, Width: bw, Height: bh, Confidence: hollowness}
		stat := RingStat{Box: box, Area: areaFrac, Hollow: hollowness}
		// A ring is a thin border around a tile: nearly all its pixels
		// hug the box's edges, and it runs along all four of them. Patches
		// of a ring-colored virtual background are hollow-ish blobs that
		// fail one or the other.
		stat.Border, stat.Cover = borderShape(labels, width, ci+1, st)
		stat.Ring = areaFrac >= cfg.MinAreaFraction && areaFrac <= cfg.MaxAreaFraction &&
			hollowness >= hollownessFloor && stat.Border >= minBorderShare && stat.Cover >= minEdgeCover
		stats = append(stats, stat)
		if stat.Ring {
			candidates = append(candidates, box)
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Area > stats[j].Area })
	if len(stats) > maxRingStats {
		stats = stats[:maxRingStats]
	}
	return candidates, stats
}

// DetectRings returns every plausible ring candidate in the frame (see
// DetectRing): one is the active speaker, more is ambiguous.
func DetectRings(pix []byte, width, height int, cfg RingConfig) []RingMatch {
	return ringCandidates(pix, width, height, cfg)
}

// connectedComponents labels each true pixel in mask with its
// component number (1-indexed; 0 means "not part of any component"),
// using 4-connectivity flood fill.
func connectedComponents(mask []bool, width, height int) (labels []int, numComponents int) {
	labels = make([]int, width*height)
	visited := make([]bool, width*height)
	stack := make([]int, 0, 1024)

	for start := 0; start < width*height; start++ {
		if !mask[start] || visited[start] {
			continue
		}
		numComponents++
		visited[start] = true
		labels[start] = numComponents
		stack = append(stack[:0], start)

		for len(stack) > 0 {
			idx := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := idx%width, idx/width

			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || nx >= width || ny < 0 || ny >= height {
					continue
				}
				nidx := ny*width + nx
				if mask[nidx] && !visited[nidx] {
					visited[nidx] = true
					labels[nidx] = numComponents
					stack = append(stack, nidx)
				}
			}
		}
	}
	return labels, numComponents
}

// A ring's pixels must be at least minBorderShare within the border band
// of their bounding box, and cover at least minEdgeCover of each edge
// (rounded corners leave the ends of each edge empty).
const (
	minBorderShare = 0.9
	minEdgeCover   = 0.7
)

// borderShape measures how much component id (bounding box st) looks like
// a thin rectangular border: share is the fraction of its pixels within a
// few pixels of the box's edges, cover the least-covered edge's fraction.
func borderShape(labels []int, width, id int, st componentBox) (share, cover float64) {
	bw, bh := st.maxX-st.minX+1, st.maxY-st.minY+1
	band := max(4, min(bw, bh)/25)
	inBand := 0
	top := make([]bool, bw)
	bottom := make([]bool, bw)
	left := make([]bool, bh)
	right := make([]bool, bh)
	for y := st.minY; y <= st.maxY; y++ {
		row := labels[y*width : (y+1)*width]
		for x := st.minX; x <= st.maxX; x++ {
			if row[x] != id {
				continue
			}
			dx, dy := x-st.minX, y-st.minY
			nearL, nearR := dx < band, st.maxX-x < band
			nearT, nearB := dy < band, st.maxY-y < band
			if nearL || nearR || nearT || nearB {
				inBand++
			}
			if nearT {
				top[dx] = true
			}
			if nearB {
				bottom[dx] = true
			}
			if nearL {
				left[dy] = true
			}
			if nearR {
				right[dy] = true
			}
		}
	}
	frac := func(v []bool) float64 {
		n := 0
		for _, b := range v {
			if b {
				n++
			}
		}
		return float64(n) / float64(len(v))
	}
	cover = min(frac(top), frac(bottom), frac(left), frac(right))
	return float64(inBand) / float64(st.count), cover
}

// componentBox is one connected component's bounding box and pixel count.
type componentBox struct {
	minX, minY, maxX, maxY, count int
}

// componentStats computes every component's bounding box and pixel count
// in a single pass over labels (index i holds component i+1). A pass per
// component would cost O(pixels × components), which on a Retina frame
// with a few thousand ring-colored specks takes seconds per poll.
func componentStats(labels []int, width, numComponents int) []componentBox {
	stats := make([]componentBox, numComponents)
	for i := range stats {
		stats[i] = componentBox{minX: 1<<31 - 1, minY: 1<<31 - 1, maxX: -1, maxY: -1}
	}
	for idx, l := range labels {
		if l == 0 {
			continue
		}
		st := &stats[l-1]
		x, y := idx%width, idx/width
		st.minX = min(st.minX, x)
		st.maxX = max(st.maxX, x)
		st.minY = min(st.minY, y)
		st.maxY = max(st.maxY, y)
		st.count++
	}
	return stats
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}
