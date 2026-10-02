package videohint

import "testing"

func TestSameTile(t *testing.T) {
	a := RingMatch{X: 100, Y: 100, Width: 300, Height: 200}
	if !sameTile(a, RingMatch{X: 103, Y: 98, Width: 296, Height: 203}) {
		t.Error("a ring a few pixels off is a different tile")
	}
	if sameTile(a, RingMatch{X: 500, Y: 100, Width: 300, Height: 200}) {
		t.Error("the next tile over counts as the same tile")
	}
}

func TestSpeakerView(t *testing.T) {
	w, h := 400, 300
	gallery := &frame{width: w, height: h, pix: make([]byte, w*h*3)}
	for i := range gallery.pix {
		gallery.pix[i] = uint8(stageBackground) // Teams' flat stage background
	}
	if speakerView(gallery) {
		t.Error("a flat stage-background margin read as speaker view")
	}
	video := &frame{width: w, height: h, pix: make([]byte, w*h*3)}
	for i := range video.pix {
		video.pix[i] = 230 // a pale virtual background up to the edge
	}
	if !speakerView(video) {
		t.Error("video at the stage edge not read as speaker view")
	}
}

func TestEncodeJPEGScalesDown(t *testing.T) {
	w, h := 1000, 500
	pix := make([]byte, w*h*3)
	b, err := encodeJPEG(pix, w, h, thumbWidth, 70)
	if err != nil || len(b) == 0 {
		t.Fatalf("encodeJPEG: %v", err)
	}
}

func TestSigDiffTellsLayoutFromMotion(t *testing.T) {
	const w, h = 480, 270
	solid := func(v byte) []byte {
		p := make([]byte, w*h*3)
		for i := range p {
			p[i] = v
		}
		return p
	}
	a := lumaSig(solid(40), w, h)
	if d := sigDiff(a, lumaSig(solid(40), w, h)); d != 0 {
		t.Errorf("identical frames differ by %v", d)
	}
	// A small bright patch (a ring or a moving face) stays under keepDiff.
	small := solid(40)
	for y := 100; y < 130; y++ {
		for x := 200; x < 240; x++ {
			i := (y*w + x) * 3
			small[i], small[i+1], small[i+2] = 255, 255, 255
		}
	}
	if d := sigDiff(a, lumaSig(small, w, h)); d > keepDiff {
		t.Errorf("a small patch differs by %v, over keepDiff %v", d, keepDiff)
	}
	// Half the window changing (a layout change) is far over it.
	half := solid(40)
	for y := 0; y < h/2; y++ {
		for x := 0; x < w*3; x++ {
			half[y*w*3+x] = 200
		}
	}
	if d := sigDiff(a, lumaSig(half, w, h)); d <= keepDiff {
		t.Errorf("half the window changing differs by only %v", d)
	}
	if d := sigDiff(a, nil); d != 255 {
		t.Errorf("incomparable signatures differ by %v, want 255", d)
	}
}

func TestIsBlank(t *testing.T) {
	const w, h = 200, 100
	black := make([]byte, w*h*3)
	if !isBlank(black, w, h) {
		t.Error("an all-black frame isn't blank")
	}
	faint := make([]byte, w*h*3)
	for i := range faint {
		faint[i] = 5 // codec noise floor
	}
	if !isBlank(faint, w, h) {
		t.Error("a near-black frame isn't blank")
	}
	dark := make([]byte, w*h*3)
	for i := range dark {
		dark[i] = 29 // Teams' dark stage background
	}
	if isBlank(dark, w, h) {
		t.Error("Teams' dark stage background counts as blank")
	}
	if isBlank(nil, 0, 0) {
		t.Error("an empty frame counts as blank")
	}
}
