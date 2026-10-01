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
