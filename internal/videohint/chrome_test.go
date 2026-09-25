package videohint

import "testing"

// drawMarker builds a packed RGB buffer filled with bgColor, with a
// w x h block of markerColor placed at (x,y).
func drawMarker(width, height int, bgColor [3]uint8, x, y, w, h int, markerColor [3]uint8) []byte {
	pix := make([]byte, width*height*3)
	for i := 0; i < width*height; i++ {
		pix[i*3], pix[i*3+1], pix[i*3+2] = bgColor[0], bgColor[1], bgColor[2]
	}
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			if px < 0 || px >= width || py < 0 || py >= height {
				continue
			}
			idx := (py*width + px) * 3
			pix[idx], pix[idx+1], pix[idx+2] = markerColor[0], markerColor[1], markerColor[2]
		}
	}
	return pix
}

func teamsChromeMarker() ChromeMarker {
	return ChromeMarker{
		SearchX0: -100, SearchX1: -5,
		SearchY0: 44, SearchY1: 63,
		HueDegrees: 358, HueTolerance: 20,
		MinSaturation: 0.30, MinValue: 0.20,
		MinPixels: 40, MaxPixels: 250,
	}
}

func TestDetectCallChrome_FindsMarkerInSearchWindow(t *testing.T) {
	const width, height = 1804, 1128
	// Real calibrated position/size (see rule.go's Teams entry).
	pix := drawMarker(width, height, [3]uint8{20, 20, 20}, width-52, 50, 20, 8, [3]uint8{180, 102, 104})

	if !DetectCallChrome(pix, width, height, teamsChromeMarker()) {
		t.Error("DetectCallChrome() = false, want true (marker present at calibrated position)")
	}
}

func TestDetectCallChrome_AbsentWhenNoMarker(t *testing.T) {
	const width, height = 1804, 1128
	pix := make([]byte, width*height*3)
	for i := 0; i < width*height; i++ {
		pix[i*3], pix[i*3+1], pix[i*3+2] = 20, 20, 20
	}

	if DetectCallChrome(pix, width, height, teamsChromeMarker()) {
		t.Error("DetectCallChrome() = true, want false (no marker anywhere in frame)")
	}
}

func TestDetectCallChrome_IgnoresMatchOutsideSearchWindow(t *testing.T) {
	const width, height = 1804, 1128
	// Same marker color/size as the calibrated case, but placed near
	// the top-right corner well outside the Y search band — this is
	// exactly the real false-positive found live (a chat window's
	// notification badge at y~20-37, distinct from the real Leave
	// icon's y~50-57).
	pix := drawMarker(width, height, [3]uint8{20, 20, 20}, width-40, 20, 20, 15, [3]uint8{180, 102, 104})

	if DetectCallChrome(pix, width, height, teamsChromeMarker()) {
		t.Error("DetectCallChrome() = true, want false (marker outside the calibrated search window)")
	}
}

func TestDetectCallChrome_UnconfiguredReturnsFalse(t *testing.T) {
	pix := make([]byte, 100*100*3)
	var m ChromeMarker // zero value
	if DetectCallChrome(pix, 100, 100, m) {
		t.Error("DetectCallChrome() with unconfigured ChromeMarker = true, want false")
	}
}

func TestDetectCallChrome_ToleratesBrightnessShift(t *testing.T) {
	// Same hue/saturation as the calibrated marker but noticeably
	// brighter (a stand-in for a light-mode/theme brightness shift,
	// since there's no real light-mode capture to calibrate against
	// yet — see ChromeMarker's doc comment) should still match, since
	// matching is hue+saturation based and deliberately ignores value.
	const width, height = 1804, 1128
	brighter := [3]uint8{230, 130, 133} // same hue/sat family, higher value
	pix := drawMarker(width, height, [3]uint8{20, 20, 20}, width-52, 50, 20, 8, brighter)

	if !DetectCallChrome(pix, width, height, teamsChromeMarker()) {
		t.Error("DetectCallChrome() = false, want true (brightness shift alone shouldn't defeat a hue-based match)")
	}
}

func TestHueDist_HandlesWraparound(t *testing.T) {
	if d := hueDist(358, 2); d != 4 {
		t.Errorf("hueDist(358, 2) = %v, want 4", d)
	}
	if d := hueDist(10, 20); d != 10 {
		t.Errorf("hueDist(10, 20) = %v, want 10", d)
	}
}
