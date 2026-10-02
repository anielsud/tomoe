// Package ocr reads a line of text from an image with an open text
// recognition model run through ONNX Runtime (internal/onnxrt), the same on
// every platform. It replaces Apple's Vision framework for reading names off
// a meeting window.
//
// The model is a PaddleOCR PP-OCRv5 recognizer (Apache-2.0) converted to
// ONNX by RapidOCR: one image of a single text line in, one probability per
// character class per horizontal step out, decoded greedily (CTC). It
// carries its own character list in its metadata. Reading a whole page
// would need a separate text-detection model; Tomoe only ever reads a line
// it has already cropped (a tile's name label, a toolbar strip), so it
// doesn't.
package ocr

import (
	"fmt"
	"math"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/sosuke-ai/tomoe-pc/internal/onnxrt"
)

const (
	// inputHeight is the line height the model was trained on.
	inputHeight = 48
	// maxInputWidth bounds the resized line (a very wide crop is mostly
	// background, and the model's cost grows with width).
	maxInputWidth = 2400
	// maxCharacters bounds the character list read from model metadata.
	maxCharacters = 20000
)

// Recognizer reads text lines. Safe for concurrent use.
type Recognizer struct {
	mu    sync.Mutex
	sess  *ort.DynamicAdvancedSession
	in    string
	out   string
	chars []string // class i+1 is chars[i]; class 0 is the CTC blank
}

// Open loads the recognition model at path.
func Open(path string) (*Recognizer, error) {
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
	md, err := ort.GetModelMetadata(path)
	if err != nil {
		return nil, fmt.Errorf("ocr: reading model metadata: %w", err)
	}
	defer md.Destroy()
	raw, ok, err := md.LookupCustomMetadataMap("character")
	if err != nil || !ok {
		return nil, fmt.Errorf("ocr: model %s has no character list in its metadata", path)
	}
	chars := parseCharacters(raw)
	if len(chars) == 0 || len(chars) > maxCharacters {
		return nil, fmt.Errorf("ocr: model %s has an unusable character list (%d entries)", path, len(chars))
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
	return &Recognizer{sess: sess, in: inputs[0].Name, out: outputs[0].Name, chars: chars}, nil
}

// parseCharacters splits the model's character list (one per line) and adds
// the space the model was trained with as its last class.
func parseCharacters(raw string) []string {
	var chars []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line != "" {
			chars = append(chars, line)
		}
	}
	return append(chars, " ")
}

// Close releases the model.
func (r *Recognizer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess != nil {
		r.sess.Destroy()
		r.sess = nil
	}
}

// Recognize reads the text in pix, a packed RGB image (3 bytes per pixel, no
// row padding) of one line of text, and returns it with a confidence (the
// mean probability of its characters, 0-1).
func (r *Recognizer) Recognize(pix []byte, width, height int) (string, float64, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return "", 0, fmt.Errorf("ocr: pixel buffer too small for %dx%d RGB", width, height)
	}
	in, w := preprocess(pix, width, height)
	x, err := ort.NewTensor(ort.NewShape(1, 3, inputHeight, int64(w)), in)
	if err != nil {
		return "", 0, err
	}
	defer x.Destroy()
	outputs := []ort.Value{nil}

	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return "", 0, fmt.Errorf("ocr: recognizer closed")
	}
	err = r.sess.Run([]ort.Value{x}, outputs)
	r.mu.Unlock()
	if err != nil {
		return "", 0, fmt.Errorf("ocr: running model: %w", err)
	}
	defer outputs[0].Destroy()
	probs, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return "", 0, fmt.Errorf("ocr: unexpected output type %T", outputs[0])
	}
	shape := probs.GetShape()
	if len(shape) != 3 || shape[0] != 1 {
		return "", 0, fmt.Errorf("ocr: unexpected output shape %v", shape)
	}
	text, conf := decode(probs.GetData(), int(shape[1]), int(shape[2]), r.chars)
	return text, conf, nil
}

// preprocess scales the line to inputHeight (keeping its aspect ratio, up
// to maxInputWidth), and lays it out as the model expects: planes of B, G,
// R (PaddleOCR's models are trained on OpenCV's channel order), each pixel
// scaled to [-1, 1].
func preprocess(pix []byte, width, height int) ([]float32, int) {
	w := int(math.Ceil(float64(inputHeight) * float64(width) / float64(height)))
	w = max(8, min(w, maxInputWidth))
	out := make([]float32, 3*inputHeight*w)
	plane := inputHeight * w
	sx := float64(width) / float64(w)
	sy := float64(height) / float64(inputHeight)
	for y := 0; y < inputHeight; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0 := clampInt(int(math.Floor(fy)), 0, height-1)
		y1 := clampInt(y0+1, 0, height-1)
		wy := fy - math.Floor(fy)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0 := clampInt(int(math.Floor(fx)), 0, width-1)
			x1 := clampInt(x0+1, 0, width-1)
			wx := fx - math.Floor(fx)
			for c := 0; c < 3; c++ {
				p := func(xx, yy int) float64 { return float64(pix[(yy*width+xx)*3+c]) }
				v := (1-wy)*((1-wx)*p(x0, y0)+wx*p(x1, y0)) + wy*((1-wx)*p(x0, y1)+wx*p(x1, y1))
				// c is R, G, B; the model wants B, G, R.
				out[(2-c)*plane+y*w+x] = float32(v/127.5 - 1)
			}
		}
	}
	return out, w
}

// decode reads the most likely class at each of steps positions (classes
// per step), dropping repeats and the blank (class 0): CTC greedy decoding.
func decode(probs []float32, steps, classes int, chars []string) (string, float64) {
	var b strings.Builder
	var sum float64
	n := 0
	prev := -1
	for t := 0; t < steps; t++ {
		row := probs[t*classes : (t+1)*classes]
		best, bp := 0, row[0]
		for c, p := range row {
			if p > bp {
				best, bp = c, p
			}
		}
		if best != 0 && best != prev && best-1 < len(chars) {
			b.WriteString(chars[best-1])
			sum += float64(bp)
			n++
		}
		prev = best
	}
	if n == 0 {
		return "", 0
	}
	return strings.TrimSpace(b.String()), sum / float64(n)
}

func clampInt(v, lo, hi int) int { return max(lo, min(v, hi)) }
