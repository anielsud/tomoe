package ocr

import (
	"fmt"
	"math"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/sosuke-ai/tomoe-pc/internal/onnxrt"
)

// Text detection: a PaddleOCR PP-OCRv5 detector (DBNet, Apache-2.0, ONNX by
// RapidOCR) turns an image into a per-pixel text probability map; regions
// of it become boxes, one per word or line. Recognizing a box rather than a
// fixed crop is what makes reading robust to where a label happens to sit
// (a gallery tile, a speaker-view stage, a toolbar).

const (
	// detTextHeight is the text height (pixels) small crops are scaled to:
	// UI labels are 10-16 px tall, and the detector is reliable from ~24.
	detTextHeight = 32
	// detMaxSide bounds the scaled image's longer side (cost grows with
	// area).
	detMaxSide = 1600
	// DB post-processing, as PaddleOCR's defaults: a pixel is text above
	// detThresh, a region is kept if its mean probability is above
	// detBoxThresh, and boxes grow by detUnclip (they're trained shrunk).
	detThresh    = 0.3
	detBoxThresh = 0.6
	detUnclip    = 1.5
	detMinSide   = 3
)

// Box is a text region in the source image's pixels.
type Box struct {
	X, Y, W, H int
	Score      float64
}

// Detector finds text regions. Safe for concurrent use.
type Detector struct {
	mu   sync.Mutex
	sess *ort.DynamicAdvancedSession
}

// OpenDetector loads the detection model at path.
func OpenDetector(path string) (*Detector, error) {
	if err := onnxrt.Init(); err != nil {
		return nil, err
	}
	inputs, outputs, err := ort.GetInputOutputInfo(path)
	if err != nil {
		return nil, fmt.Errorf("ocr: reading model %s: %w", path, err)
	}
	if len(inputs) != 1 || len(outputs) != 1 {
		return nil, fmt.Errorf("ocr: model %s has %d inputs and %d outputs, want 1 and 1", path, len(inputs), len(outputs))
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	_ = opts.SetIntraOpNumThreads(1)
	_ = opts.SetInterOpNumThreads(1)
	sess, err := ort.NewDynamicAdvancedSession(path, []string{inputs[0].Name}, []string{outputs[0].Name}, opts)
	if err != nil {
		return nil, fmt.Errorf("ocr: loading model %s: %w", path, err)
	}
	return &Detector{sess: sess}, nil
}

// Close releases the model.
func (d *Detector) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sess != nil {
		d.sess.Destroy()
		d.sess = nil
	}
}

// Detect returns the text regions in pix (packed RGB), each a word or a
// line, in the source image's pixels.
func (d *Detector) Detect(pix []byte, width, height int) ([]Box, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return nil, fmt.Errorf("ocr: pixel buffer too small for %dx%d RGB", width, height)
	}
	// Scale so a small crop's text reaches detTextHeight (taking the
	// crop's height as an upper bound on the text's), within detMaxSide,
	// then round each side to the model's stride of 32.
	scale := math.Max(1, float64(detTextHeight)*3/float64(height))
	scale = math.Min(scale, float64(detMaxSide)/float64(max(width, height)))
	sw := max(32, int(math.Round(float64(width)*scale/32))*32)
	sh := max(32, int(math.Round(float64(height)*scale/32))*32)
	in := detInput(pix, width, height, sw, sh)
	x, err := ort.NewTensor(ort.NewShape(1, 3, int64(sh), int64(sw)), in)
	if err != nil {
		return nil, err
	}
	defer x.Destroy()
	outputs := []ort.Value{nil}
	d.mu.Lock()
	if d.sess == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("ocr: detector closed")
	}
	err = d.sess.Run([]ort.Value{x}, outputs)
	d.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("ocr: running detector: %w", err)
	}
	defer outputs[0].Destroy()
	probs, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("ocr: unexpected detector output type %T", outputs[0])
	}
	shape := probs.GetShape()
	if len(shape) != 4 || shape[0] != 1 || shape[1] != 1 {
		return nil, fmt.Errorf("ocr: unexpected detector output shape %v", shape)
	}
	mh, mw := int(shape[2]), int(shape[3])
	boxes := dbBoxes(probs.GetData(), mw, mh)
	// Back to source pixels.
	fx, fy := float64(width)/float64(mw), float64(height)/float64(mh)
	out := make([]Box, 0, len(boxes))
	for _, b := range boxes {
		x0 := clampInt(int(math.Floor(float64(b.X)*fx)), 0, width-1)
		y0 := clampInt(int(math.Floor(float64(b.Y)*fy)), 0, height-1)
		x1 := clampInt(int(math.Ceil(float64(b.X+b.W)*fx)), x0+1, width)
		y1 := clampInt(int(math.Ceil(float64(b.Y+b.H)*fy)), y0+1, height)
		out = append(out, Box{X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Score: b.Score})
	}
	return out, nil
}

// detInput resizes pix to sw x sh and normalizes it as PaddleOCR's DB
// detector expects: channels in OpenCV's B, G, R order, each scaled to 0-1
// and standardized with ImageNet's mean and deviation (applied in that same
// channel order, as PaddleOCR does).
func detInput(pix []byte, width, height, sw, sh int) []float32 {
	mean := [3]float64{0.485, 0.456, 0.406}
	std := [3]float64{0.229, 0.224, 0.225}
	out := make([]float32, 3*sw*sh)
	plane := sw * sh
	sx, sy := float64(width)/float64(sw), float64(height)/float64(sh)
	for y := 0; y < sh; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0 := clampInt(int(math.Floor(fy)), 0, height-1)
		y1 := clampInt(y0+1, 0, height-1)
		wy := fy - math.Floor(fy)
		for x := 0; x < sw; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0 := clampInt(int(math.Floor(fx)), 0, width-1)
			x1 := clampInt(x0+1, 0, width-1)
			wx := fx - math.Floor(fx)
			for c := 0; c < 3; c++ { // c: R, G, B in pix
				p := func(xx, yy int) float64 { return float64(pix[(yy*width+xx)*3+c]) }
				v := (1-wy)*((1-wx)*p(x0, y0)+wx*p(x1, y0)) + wy*((1-wx)*p(x0, y1)+wx*p(x1, y1))
				ch := 2 - c // B, G, R
				out[ch*plane+y*sw+x] = float32((v/255 - mean[ch]) / std[ch])
			}
		}
	}
	return out
}

// dbBoxes turns a probability map into boxes (map pixels): connected
// regions above detThresh whose mean probability passes detBoxThresh,
// grown by the unclip distance (area * ratio / perimeter).
func dbBoxes(prob []float32, w, h int) []Box {
	seen := make([]bool, w*h)
	var out []Box
	stack := make([]int, 0, 256)
	for start := range prob {
		if seen[start] || prob[start] <= detThresh {
			continue
		}
		minX, minY, maxX, maxY := w, h, -1, -1
		var sum float64
		n := 0
		stack = append(stack[:0], start)
		seen[start] = true
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%w, i/w
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
			sum += float64(prob[i])
			n++
			for _, nb := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+nb[0], y+nb[1]
				if nx < 0 || ny < 0 || nx >= w || ny >= h {
					continue
				}
				j := ny*w + nx
				if !seen[j] && prob[j] > detThresh {
					seen[j] = true
					stack = append(stack, j)
				}
			}
		}
		bw, bh := maxX-minX+1, maxY-minY+1
		if bw < detMinSide || bh < detMinSide || sum/float64(n) < detBoxThresh {
			continue
		}
		d := float64(bw*bh) * detUnclip / float64(2*(bw+bh))
		x0 := clampInt(int(math.Floor(float64(minX)-d)), 0, w-1)
		y0 := clampInt(int(math.Floor(float64(minY)-d)), 0, h-1)
		x1 := clampInt(int(math.Ceil(float64(maxX+1)+d)), x0+1, w)
		y1 := clampInt(int(math.Ceil(float64(maxY+1)+d)), y0+1, h)
		out = append(out, Box{X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Score: sum / float64(n)})
	}
	return out
}

// Lines joins boxes into text lines: boxes that overlap vertically by at
// least half the shorter one's height and sit within one line height of
// each other horizontally are words of one line. Lines come back top to
// bottom, left to right.
func Lines(boxes []Box) []Box {
	bs := append([]Box(nil), boxes...)
	sort.Slice(bs, func(i, j int) bool { return bs[i].X < bs[j].X })
	var lines []Box
	for _, b := range bs {
		joined := false
		for i := range lines {
			l := &lines[i]
			overlap := min(l.Y+l.H, b.Y+b.H) - max(l.Y, b.Y)
			gap := b.X - (l.X + l.W)
			if overlap*2 >= min(l.H, b.H) && gap <= max(l.H, b.H) {
				x1, y1 := max(l.X+l.W, b.X+b.W), max(l.Y+l.H, b.Y+b.H)
				l.X, l.Y = min(l.X, b.X), min(l.Y, b.Y)
				l.W, l.H = x1-l.X, y1-l.Y
				l.Score = math.Max(l.Score, b.Score)
				joined = true
				break
			}
		}
		if !joined {
			lines = append(lines, b)
		}
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Y+lines[i].H/2 < lines[j].Y || lines[j].Y+lines[j].H/2 < lines[i].Y {
			return lines[i].Y < lines[j].Y
		}
		return lines[i].X < lines[j].X
	})
	return lines
}
