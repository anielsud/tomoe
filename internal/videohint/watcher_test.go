package videohint

import (
	"testing"
	"time"
)

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

func TestUITrackerFlagsAFrozenTimerRegion(t *testing.T) {
	const w, h = 400, 120
	frame := func(tick byte) []byte {
		p := make([]byte, w*h*3)
		for y := uiY0 + 5; y < uiY0+20; y++ { // the timer's digits
			for x := uiX0 + 40; x < uiX0+80; x++ {
				p[(y*w+x)*3] = tick
			}
		}
		return p
	}
	var u uiTracker
	t0 := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	// A live timer changes every second: never frozen.
	for s := 0; s < 10; s++ {
		if d := u.update(frame(byte(s+1)), w, h, t0.Add(time.Duration(s)*time.Second)); d != 0 {
			t.Fatalf("live timer reported unchanged for %v at second %d", d, s)
		}
	}
	// The same pixels for 6 s: frozen after uiFrozenAfter, and it resumes
	// the moment the timer changes.
	var last time.Duration
	for s := 10; s < 16; s++ {
		last = u.update(frame(99), w, h, t0.Add(time.Duration(s)*time.Second))
	}
	if last < uiFrozenAfter {
		t.Errorf("a timer unchanged for %v isn't flagged (threshold %v)", last, uiFrozenAfter)
	}
	if d := u.update(frame(100), w, h, t0.Add(16*time.Second)); d != 0 {
		t.Errorf("a changed timer is still reported frozen for %v", d)
	}
	if d := u.update(make([]byte, 10), 2, 2, t0.Add(17*time.Second)); d != 0 {
		t.Errorf("a frame too small for the region reported frozen for %v", d)
	}
}
