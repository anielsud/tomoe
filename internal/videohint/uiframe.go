package videohint

import "time"

// uiFrozenAfter is how long the call timer must stay unchanged before the
// window counts as not repainting. The timer ticks every second, so a few
// seconds with no change can't be a live interface.
const uiFrozenAfter = 4 * time.Second

// The call timer and the icons beside it, at the toolbar's left edge (Teams
// draws the timer at the same place in gallery, speaker and share layouts).
const (
	uiX0, uiY0, uiX1, uiY1 = 20, 40, 200, 84
)

// uiTracker notices a window whose interface has stopped repainting: the
// toolbar's timer region is identical look after look.
type uiTracker struct {
	hash  uint64
	since time.Time
	seen  bool
}

// update records the frame's timer region at time at and returns how long
// it has gone unchanged (0 if it just changed, or the frame is too small
// to have the region).
func (u *uiTracker) update(pix []byte, width, height int, at time.Time) time.Duration {
	if width < uiX1 || height < uiY1 || len(pix) < width*height*3 {
		u.seen = false
		return 0
	}
	h := uint64(14695981039346656037)
	for y := uiY0; y < uiY1; y++ {
		row := pix[(y*width+uiX0)*3 : (y*width+uiX1)*3]
		for _, b := range row {
			h = (h ^ uint64(b)) * 1099511628211
		}
	}
	if !u.seen || h != u.hash {
		u.hash, u.since, u.seen = h, at, true
		return 0
	}
	return at.Sub(u.since)
}
