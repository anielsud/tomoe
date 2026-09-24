package videohint

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
// shape as teamsvideo.Frame.Pix). Returns the best candidate, or (nil,
// false) if cfg is unconfigured (the zero value — see RingConfig) or
// nothing plausible is found.
//
// Algorithm: color-threshold every pixel against cfg.TargetColor within
// cfg.ColorTolerance, connected-components label the resulting mask
// (4-connectivity flood fill), then for each component check it's
// plausibly ring-shaped (its pixel count is well below its bounding
// box's full area — a filled blob wouldn't be) and within
// cfg.MinAreaFraction/MaxAreaFraction of the whole frame.
func DetectRing(pix []byte, width, height int, cfg RingConfig) (*RingMatch, bool) {
	if !cfg.configured() || width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return nil, false
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
		return nil, false
	}

	frameArea := float64(width * height)
	var best *RingMatch
	var bestScore float64

	for comp := 1; comp <= numComponents; comp++ {
		minX, minY, maxX, maxY, count := boundingBox(labels, width, comp)
		if count == 0 {
			continue
		}

		areaFrac := float64(count) / frameArea
		if areaFrac < cfg.MinAreaFraction || areaFrac > cfg.MaxAreaFraction {
			continue
		}

		bw, bh := maxX-minX+1, maxY-minY+1
		fullArea := float64(bw * bh)
		hollowness := 1.0 - float64(count)/fullArea
		if hollowness < hollownessFloor {
			continue
		}

		score := hollowness * areaFrac
		if best == nil || score > bestScore {
			best = &RingMatch{X: minX, Y: minY, Width: bw, Height: bh, Confidence: hollowness}
			bestScore = score
		}
	}

	if best == nil {
		return nil, false
	}
	return best, true
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

// boundingBox computes the bounding box and pixel count of component
// comp within a width-wide label buffer.
func boundingBox(labels []int, width, comp int) (minX, minY, maxX, maxY, count int) {
	minX, minY = 1<<31-1, 1<<31-1
	maxX, maxY = -1, -1
	for idx, l := range labels {
		if l != comp {
			continue
		}
		x, y := idx%width, idx/width
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
		count++
	}
	return
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}
