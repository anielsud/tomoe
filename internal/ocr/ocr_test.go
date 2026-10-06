package ocr

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCharactersAddsSpace(t *testing.T) {
	got := parseCharacters("a\nb\r\nc\n")
	want := []string{"a", "b", "c", " "}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestDecodeDropsBlanksAndRepeats(t *testing.T) {
	chars := []string{"a", "b", " "}
	// classes: 0 blank, 1 a, 2 b, 3 space; steps: a a blank a b space b
	steps := []int{1, 1, 0, 1, 2, 3, 2}
	probs := make([]float32, len(steps)*4)
	for t, c := range steps {
		probs[t*4+c] = 0.9
	}
	text, conf := decode(probs, len(steps), 4, chars)
	if text != "aab b" {
		t.Errorf("decode = %q, want %q", text, "aab b")
	}
	if math.Abs(conf-0.9) > 1e-6 {
		t.Errorf("confidence = %v, want 0.9", conf)
	}
	if text, _ := decode(make([]float32, 8), 2, 4, chars); text != "" {
		t.Errorf("all-blank decode = %q", text)
	}
}

func TestDBBoxesFindsRegionsAndDropsFaintOnes(t *testing.T) {
	const w, h = 40, 12
	prob := make([]float32, w*h)
	for y := 4; y < 8; y++ {
		for x := 5; x < 20; x++ {
			prob[y*w+x] = 0.95 // a word
		}
		for x := 30; x < 34; x++ {
			prob[y*w+x] = 0.35 // above the pixel threshold, below the box one
		}
	}
	boxes := dbBoxes(prob, w, h)
	if len(boxes) != 1 {
		t.Fatalf("boxes = %+v, want one", boxes)
	}
	b := boxes[0]
	if b.X > 5 || b.X+b.W < 20 || b.Y > 4 || b.Y+b.H < 8 {
		t.Errorf("box %+v doesn't cover the word (5..20 x 4..8) after unclipping", b)
	}
}

func TestLinesJoinsWordsOfALine(t *testing.T) {
	lines := Lines([]Box{
		{X: 60, Y: 10, W: 40, H: 14}, // second word
		{X: 0, Y: 11, W: 50, H: 14},  // first word, a gap smaller than a line height
		{X: 0, Y: 40, W: 30, H: 14},  // the next line
	})
	if len(lines) != 2 {
		t.Fatalf("lines = %+v, want two", lines)
	}
	if lines[0].X != 0 || lines[0].W != 100 {
		t.Errorf("first line %+v, want the two words joined (x 0, width 100)", lines[0])
	}
	if lines[1].Y != 40 {
		t.Errorf("second line %+v, want the one at y 40", lines[1])
	}
}

// With the real models (TOMOE_OCR_MODELS = a directory holding them, and
// ONNX Runtime findable), a rendered line of text reads back.
func TestReaderReadsRenderedText(t *testing.T) {
	dir := os.Getenv("TOMOE_OCR_MODELS")
	if dir == "" {
		t.Skip("set TOMOE_OCR_MODELS to the directory with the PP-OCRv5 models")
	}
	r, err := OpenReader(filepath.Join(dir, "ch_PP-OCRv5_det_mobile.onnx"), filepath.Join(dir, "en_PP-OCRv5_rec_mobile.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pix, w, h := renderText("ALEX KIM", 3)
	got, err := r.ReadText(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	// Compared as Tomoe compares names: a blocky all-caps bitmap font can
	// read back in mixed case.
	if !strings.EqualFold(got, "ALEX KIM") {
		t.Errorf("read %q, want %q", got, "ALEX KIM")
	}
}
