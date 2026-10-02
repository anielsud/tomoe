package ocr

import "strings"

// Line is one line of text read from an image.
type Line struct {
	Box
	Text       string
	Confidence float64
}

// Reader finds text lines in an image and reads them. Safe for concurrent
// use.
type Reader struct {
	det *Detector
	rec *Recognizer
}

// OpenReader loads the detection and recognition models.
func OpenReader(detPath, recPath string) (*Reader, error) {
	det, err := OpenDetector(detPath)
	if err != nil {
		return nil, err
	}
	rec, err := Open(recPath)
	if err != nil {
		det.Close()
		return nil, err
	}
	return &Reader{det: det, rec: rec}, nil
}

// Close releases both models.
func (r *Reader) Close() {
	r.det.Close()
	r.rec.Close()
}

// linePad is how far (pixels) a line's crop extends past its box: the
// recognizer was trained on lines with a little background around them.
const linePad = 2

// ReadLines returns the text lines in pix (packed RGB), top to bottom and
// left to right.
func (r *Reader) ReadLines(pix []byte, width, height int) ([]Line, error) {
	boxes, err := r.det.Detect(pix, width, height)
	if err != nil {
		return nil, err
	}
	var out []Line
	for _, b := range Lines(boxes) {
		x0, y0 := max(0, b.X-linePad), max(0, b.Y-linePad)
		x1, y1 := min(width, b.X+b.W+linePad), min(height, b.Y+b.H+linePad)
		crop := cropRGB(pix, width, x0, y0, x1-x0, y1-y0)
		text, conf, err := r.rec.Recognize(crop, x1-x0, y1-y0)
		if err != nil {
			return nil, err
		}
		if text != "" {
			out = append(out, Line{Box: b, Text: text, Confidence: conf})
		}
	}
	return out, nil
}

// ReadText returns all the text in pix, its lines joined by spaces.
func (r *Reader) ReadText(pix []byte, width, height int) (string, error) {
	lines, err := r.ReadLines(pix, width, height)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.Text
	}
	return strings.Join(parts, " "), nil
}

// cropRGB copies a rectangle (already clamped to the image) out of a packed
// RGB image.
func cropRGB(pix []byte, width, x, y, w, h int) []byte {
	out := make([]byte, w*h*3)
	for row := 0; row < h; row++ {
		src := ((y+row)*width + x) * 3
		copy(out[row*w*3:(row+1)*w*3], pix[src:src+w*3])
	}
	return out
}
