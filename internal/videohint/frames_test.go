package videohint

import (
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
)

// TestSavedFrames runs ring detection over real saved frames: set
// TOMOE_HINT_FRAMES to a folder of looks saved for analysis (or older
// escalation snapshots), each a directory with frame.jpg or frame.png and
// optionally expect.json ({"rings": N}). Frames show real meetings, so
// they stay local and this test skips without them.
func TestSavedFrames(t *testing.T) {
	root := os.Getenv("TOMOE_HINT_FRAMES")
	if root == "" {
		t.Skip("TOMOE_HINT_FRAMES not set")
	}
	rule, _ := ruleFor(meeting.PlatformTeams)
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		pix, w, h, err := loadFrame(dir)
		if err != nil {
			t.Logf("%s: %v", d.Name(), err)
			continue
		}
		chrome := DetectCallChrome(pix, w, h, rule.Chrome)
		rings := DetectRings(pix, w, h, rule.Ring)
		sv := speakerView(&frame{width: w, height: h, pix: pix})
		t.Logf("%s: %dx%d call=%v speakerView=%v rings=%d %v", d.Name(), w, h, chrome, sv, len(rings), rings)
		var want struct{ Rings *int }
		if b, err := os.ReadFile(filepath.Join(dir, "expect.json")); err == nil && json.Unmarshal(b, &want) == nil && want.Rings != nil {
			if len(rings) != *want.Rings {
				t.Errorf("%s: %d rings, want %d", d.Name(), len(rings), *want.Rings)
			}
		}
	}
}

func loadFrame(dir string) ([]byte, int, int, error) {
	var f *os.File
	var err error
	for _, name := range []string{"frame.png", "frame.jpg"} {
		if f, err = os.Open(filepath.Join(dir, name)); err == nil {
			break
		}
	}
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := (y*w + x) * 3
			pix[i], pix[i+1], pix[i+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
		}
	}
	return pix, w, h, nil
}
