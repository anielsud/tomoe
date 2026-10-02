package videohint

import "time"

// uiFrozenAfter is how long the call timer must stay unchanged before the
// window counts as not repainting. The timer ticks every second, so a few
// seconds with no change can't be a live interface.
const uiFrozenAfter = 4 * time.Second

// The call timer and the icons beside it at the toolbar's left edge, in
// points (Teams draws the timer at the same place in gallery, speaker and
// share layouts). A capture at a higher backing scale scales it.
const (
	uiX0, uiY0, uiX1, uiY1 = 20, 40, 200, 84
)

// uiTracker notices a window whose interface has stopped repainting: the
// toolbar's timer region is identical look after look.
//
// It only reports a freeze once it has seen the region change at least
// once for this window and size. If the region isn't really the timer (a
// layout or scale this rule doesn't know), it never changes, and an
// unchanging region must not read as a frozen window: that would silently
// stop every name read.
type uiTracker struct {
	hash          uint64
	since         time.Time
	seen, armed   bool
	width, height int
	scale         int
}

// update records the frame's timer region at time at and returns how long
// it has gone unchanged (0 if it just changed, if it has never been seen
// changing, or if the frame is too small to have the region). scale is the
// capture's pixels per point (1 when unknown).
func (u *uiTracker) update(pix []byte, width, height, scale int, at time.Time) time.Duration {
	scale = max(1, scale)
	x0, y0, x1, y1 := uiX0*scale, uiY0*scale, uiX1*scale, uiY1*scale
	if width < x1 || height < y1 || len(pix) < width*height*3 {
		*u = uiTracker{}
		return 0
	}
	if u.seen && (u.width != width || u.height != height || u.scale != scale) {
		*u = uiTracker{} // another window or size: start over
	}
	h := uint64(14695981039346656037)
	for y := y0; y < y1; y++ {
		for _, b := range pix[(y*width+x0)*3 : (y*width+x1)*3] {
			h = (h ^ uint64(b)) * 1099511628211
		}
	}
	switch {
	case !u.seen:
		*u = uiTracker{hash: h, since: at, seen: true, width: width, height: height, scale: scale}
		return 0
	case h != u.hash:
		u.hash, u.since, u.armed = h, at, true
		return 0
	case !u.armed:
		return 0
	}
	return at.Sub(u.since)
}
