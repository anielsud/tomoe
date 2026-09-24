package videohint

import "fmt"

// LabelRect computes the pixel rectangle of a meeting app's name-label
// overlay within a captured frame, given a matched ring and that
// platform's LabelRegion. The label spans the ring's full width.
func LabelRect(ring RingMatch, label LabelRegion) (x, y, w, h int) {
	x = ring.X
	w = ring.Width
	y = ring.Y + int(label.YFraction*float64(ring.Height))
	h = int(label.HeightFraction * float64(ring.Height))
	return x, y, w, h
}

// cropRGB copies the (x,y,w,h) rectangle out of a packed RGB frame
// buffer (no padding, 3 bytes per pixel, frameWidth*frameHeight*3
// bytes total) into its own packed RGB buffer. The rectangle is
// clamped to the frame's bounds first, since a ring detected near a
// frame edge can produce a label rectangle that runs slightly past it.
func cropRGB(pix []byte, frameWidth, frameHeight, x, y, w, h int) ([]byte, int, int, error) {
	if frameWidth <= 0 || frameHeight <= 0 || len(pix) < frameWidth*frameHeight*3 {
		return nil, 0, 0, fmt.Errorf("videohint: frame buffer too small for %dx%d RGB", frameWidth, frameHeight)
	}

	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > frameWidth {
		w = frameWidth - x
	}
	if y+h > frameHeight {
		h = frameHeight - y
	}
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("videohint: crop rectangle (%d,%d,%d,%d) is empty after clamping to %dx%d frame", x, y, w, h, frameWidth, frameHeight)
	}

	out := make([]byte, w*h*3)
	for row := 0; row < h; row++ {
		srcStart := ((y+row)*frameWidth + x) * 3
		dstStart := row * w * 3
		copy(out[dstStart:dstStart+w*3], pix[srcStart:srcStart+w*3])
	}
	return out, w, h, nil
}

// RecognizeLabel crops the name-label region implied by a matched ring
// and platform Label config out of frame, then runs OCR on it. Returns
// the recognized text (trimmed of surrounding whitespace is the
// caller's job, since RecognizeText already returns Vision's raw
// output) or an error if cropping or OCR failed.
func RecognizeLabel(pix []byte, frameWidth, frameHeight int, ring RingMatch, label LabelRegion) (string, error) {
	x, y, w, h := LabelRect(ring, label)
	crop, cw, ch, err := cropRGB(pix, frameWidth, frameHeight, x, y, w, h)
	if err != nil {
		return "", err
	}
	return RecognizeText(crop, cw, ch)
}
