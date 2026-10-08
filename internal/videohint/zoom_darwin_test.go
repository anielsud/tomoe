//go:build darwin

package videohint

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

func TestPickZoomWindow(t *testing.T) {
	home := teamsvideo.WindowInfo{ID: 1, Owner: "Zoom", Title: "Zoom Workplace", Width: 2000, Height: 1200}
	call := teamsvideo.WindowInfo{ID: 2, Owner: "Zoom", Title: "Zoom Meeting", Width: 1900, Height: 1200}
	float := teamsvideo.WindowInfo{ID: 3, Owner: "Zoom", Title: "", Width: 400, Height: 250}
	if w, ok := pickZoomWindow([]teamsvideo.WindowInfo{home, call, float}, "Zoom"); !ok || w.ID != 2 {
		t.Errorf("with the call window: got %d %v", w.ID, ok)
	}
	if w, ok := pickZoomWindow([]teamsvideo.WindowInfo{home, float}, "Zoom"); !ok || w.ID != 3 {
		t.Errorf("minimized: got %d %v", w.ID, ok)
	}
	if _, ok := pickZoomWindow([]teamsvideo.WindowInfo{home}, "Zoom"); ok {
		t.Error("home screen alone was picked")
	}
}

func TestPickZoomCall(t *testing.T) {
	home := teamsvideo.WindowInfo{ID: 1, Owner: "Zoom", Title: "Zoom Workplace", Width: 960}
	call := teamsvideo.WindowInfo{ID: 2, Owner: "Zoom", Title: "Zoom Meeting", Width: 1900}
	thumb := teamsvideo.WindowInfo{ID: 3, Owner: "Zoom", Title: "", Width: 240}
	popup := teamsvideo.WindowInfo{ID: 4, Owner: "Zoom", Title: "", Width: 900}
	other := teamsvideo.WindowInfo{ID: 5, Owner: "Notes", Title: "Zoom Meeting", Width: 800}
	for _, c := range []struct {
		ws   []teamsvideo.WindowInfo
		want int
	}{
		{[]teamsvideo.WindowInfo{home, call, thumb}, 2},
		{[]teamsvideo.WindowInfo{popup, home, thumb}, 3},
		{[]teamsvideo.WindowInfo{popup, home, other}, 0},
	} {
		w, ok := pickZoomCall(c.ws)
		if got := map[bool]int{true: int(w.ID)}[ok]; got != c.want {
			t.Errorf("pickZoomCall(%v) = %d, want %d", c.ws, got, c.want)
		}
	}
}
