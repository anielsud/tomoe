package videohint

import (
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Stretch is a span of a session when the watched window wasn't repainting
// its interface, so its speaker highlight was stale.
type Stretch struct {
	Start, End time.Time
	// Looks is how many looks fell in it; FromFrames whether it was found
	// by replaying saved frames (sessions recorded before the live check)
	// rather than from looks marked ui_frozen.
	Looks      int
	FromFrames bool
}

// stretchGap joins stale marks this close together into one stretch (saved
// frames of an unchanging window are about 10 s apart).
const stretchGap = 15 * time.Second

// FrozenStretches finds when a session's watched window stopped repainting:
// looks the live check marked ui_frozen and, for sessions recorded before it
// existed, the saved full frames (Record for tuning) replayed through the
// same check. dir is the session's directory; looks are its recorded looks.
func FrozenStretches(dir string, looks []Look) ([]Stretch, error) {
	sorted := append([]Look(nil), looks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	type mark struct {
		from, to   time.Time
		fromFrames bool
	}
	var marks []mark
	trackers := map[int]*uiTracker{}
	pointsWidth := map[int]int{} // window -> width in points, from the inventory
	for _, l := range sorted {
		for _, w := range l.Windows {
			pointsWidth[w.ID] = w.Width
		}
		if l.Stage == StageUIFrozen {
			marks = append(marks, mark{l.Time, l.Time, false})
			continue
		}
		path := filepath.Join(dir, "looks", fmt.Sprintf("%d-full.jpg", l.ID))
		if _, err := os.Stat(path); err != nil {
			continue
		}
		scale := 1
		if pw := pointsWidth[l.WindowID]; pw > 0 && l.Width > 0 {
			scale = max(1, (l.Width+pw/2)/pw)
		}
		pix, w, h, err := decodeRegion(path, scale)
		if err != nil {
			continue
		}
		u := trackers[l.WindowID]
		if u == nil {
			u = &uiTracker{}
			trackers[l.WindowID] = u
		}
		if d := u.update(pix, w, h, scale, l.Time); d >= uiFrozenAfter {
			marks = append(marks, mark{l.Time.Add(-d), l.Time, true})
		}
	}

	var out []Stretch
	for _, m := range marks {
		if n := len(out); n > 0 && !m.from.After(out[n-1].End.Add(stretchGap)) {
			if m.to.After(out[n-1].End) {
				out[n-1].End = m.to
			}
			out[n-1].FromFrames = out[n-1].FromFrames || m.fromFrames
			continue
		}
		out = append(out, Stretch{Start: m.from, End: m.to, FromFrames: m.fromFrames})
	}
	for i := range out {
		for _, l := range sorted {
			if !l.Time.Before(out[i].Start) && !l.Time.After(out[i].End) {
				out[i].Looks++
			}
		}
	}
	return out, nil
}

// decodeRegion decodes a saved frame as packed RGB, filling in only the
// timer region uiTracker reads (the rest stays zero): converting a whole
// frame pixel by pixel is the slow part.
func decodeRegion(path string, scale int) ([]byte, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*3)
	y1, x1 := min(h, uiY1*scale), min(w, uiX1*scale)
	for y := uiY0 * scale; y < y1; y++ {
		for x := uiX0 * scale; x < x1; x++ {
			r, g, bl := rgbAt(img, b.Min.X+x, b.Min.Y+y)
			i := (y*w + x) * 3
			pix[i], pix[i+1], pix[i+2] = r, g, bl
		}
	}
	return pix, w, h, nil
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}
