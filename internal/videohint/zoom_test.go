package videohint

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/axtree"
)

// zoomFrame is a dark 400x200 window frame with a green border round the
// box (x0,y0)-(x1,y1).
func zoomFrame(x0, y0, x1, y1 int) *frame {
	const w, h = 400, 200
	pix := make([]byte, w*h*3)
	set := func(x, y int) {
		i := (y*w + x) * 3
		pix[i], pix[i+1], pix[i+2] = 138, 200, 105
	}
	for x := x0; x < x1; x++ {
		for d := 0; d < 2; d++ {
			set(x, y0+d)
			set(x, y1-1-d)
		}
	}
	for y := y0; y < y1; y++ {
		for d := 0; d < 2; d++ {
			set(x0+d, y)
			set(x1-1-d, y)
		}
	}
	return &frame{width: w, height: h, pix: pix}
}

func zoomTree(tiles ...axtree.Element) []axtree.Element {
	// The window sits at (1000, 50) on screen, at half the frame's pixels
	// per point (a Retina capture).
	out := []axtree.Element{
		{Role: "AXWindow", Title: "Zoom Workplace", Rect: axtree.Rect{X: 0, Y: 0, Width: 100, Height: 100}},
		{Role: "AXTabGroup", Description: "Somebody Else, Computer audio unmuted, Video on", Rect: axtree.Rect{X: 10, Y: 10, Width: 20, Height: 20}},
		{Role: "AXWindow", Title: "Zoom Meeting", Rect: axtree.Rect{X: 1000, Y: 50, Width: 200, Height: 100}},
		{Role: "AXButton", Description: "View options, Speaker"},
	}
	return append(out, tiles...)
}

func tile(desc string, x, y float64) axtree.Element {
	return axtree.Element{Role: "AXTabGroup", Description: desc, Rect: axtree.Rect{X: x, Y: y, Width: 40, Height: 25}}
}

func TestAnalyzeZoomHighlightedTile(t *testing.T) {
	defer func(r func(int) ([]axtree.Element, error)) { readTree = r }(readTree)
	readTree = func(int) ([]axtree.Element, error) {
		return zoomTree(
			tile("Alex Kim, Computer audio muted, Video on", 1010, 60),
			tile("Ana Lopez, Computer audio unmuted, Video off", 1060, 60),
		), nil
	}
	// Ana's tile is at (60,10)-(100,35) points in the window: (120,20)-(200,70) px.
	var l Look
	(&Watcher{}).analyzeZoom(&l, zoomFrame(120, 20, 200, 70))
	if l.Stage != StageTileHighlight || l.Name != "Ana Lopez" || !l.Usable {
		t.Fatalf("got stage %s name %q usable %v (%s)", l.Stage, l.Name, l.Usable, l.Detail)
	}
	if len(l.Tiles) != 2 || !l.Tiles[1].Unmuted || l.Tiles[0].Highlight != 0 || l.Layout != "Speaker" {
		t.Errorf("tiles %+v layout %q", l.Tiles, l.Layout)
	}
}

func TestAnalyzeZoomSpeakerViewAndNone(t *testing.T) {
	defer func(r func(int) ([]axtree.Element, error)) { readTree = r }(readTree)
	readTree = func(int) ([]axtree.Element, error) {
		return zoomTree(tile("Alex Kim, Computer audio muted, Video on", 1010, 60)), nil
	}
	var l Look
	(&Watcher{}).analyzeZoom(&l, zoomFrame(0, 0, 0, 0))
	if l.Stage != StageSpeakerView || l.Name != "Alex Kim" {
		t.Errorf("one tile: got %s %q", l.Stage, l.Name)
	}

	readTree = func(int) ([]axtree.Element, error) {
		return zoomTree(tile("Alex Kim, Computer audio muted, Video on", 1010, 60), tile("Ana Lopez, Computer audio muted, Video on", 1060, 60)), nil
	}
	l = Look{}
	(&Watcher{}).analyzeZoom(&l, zoomFrame(0, 0, 0, 0))
	if l.Usable || l.Name != "" || l.Stage != StageNoRingMatch {
		t.Errorf("no highlight: got %s %q usable %v", l.Stage, l.Name, l.Usable)
	}

	readTree = func(int) ([]axtree.Element, error) { return nil, axtree.ErrNotTrusted }
	l = Look{}
	(&Watcher{}).analyzeZoom(&l, zoomFrame(0, 0, 0, 0))
	if l.Stage != StageNoTiles || l.Usable {
		t.Errorf("no permission: got %s usable %v", l.Stage, l.Usable)
	}
}

func TestAnalyzeZoomContentSpotlight(t *testing.T) {
	defer func(r func(int) ([]axtree.Element, error)) { readTree = r }(readTree)
	share := axtree.Element{Role: "AXUnknown", Description: "Share content", Rect: axtree.Rect{X: 1000, Y: 50, Width: 150, Height: 100}}
	readTree = func(int) ([]axtree.Element, error) {
		return zoomTree(share, tile("Room One, No audio connected", 1150, 60)), nil
	}
	w := &Watcher{}
	fr := zoomFrame(10, 10, 100, 80) // something on the shared screen
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	l := Look{ID: 1, Time: start}
	w.analyzeZoom(&l, fr)
	if l.Stage != StageSpeakerView || l.Name != "Room One" {
		t.Errorf("spotlight: got %s %q", l.Stage, l.Name)
	}
	if len(l.Content) == 0 || l.ContentRect == nil {
		t.Fatal("first shared screen not kept")
	}

	// Same screen, later: nothing new.
	l = Look{ID: 2, Time: start.Add(10 * time.Second)}
	w.analyzeZoom(&l, fr)
	if len(l.Content) != 0 {
		t.Errorf("repeat: content %d bytes", len(l.Content))
	}

	// A new slide is kept, but not within contentGap of the last one.
	slide := zoomFrame(0, 0, 0, 0)
	for i := range slide.pix[:len(slide.pix)/2] {
		slide.pix[i] = 230 // the top half turns white
	}
	l = Look{ID: 3, Time: start.Add(11 * time.Second)}
	w.analyzeZoom(&l, slide)
	if len(l.Content) == 0 {
		t.Error("changed screen not kept")
	}
	l = Look{ID: 4, Time: start.Add(12 * time.Second)}
	w.analyzeZoom(&l, fr)
	if len(l.Content) != 0 {
		t.Error("kept a screen within contentGap")
	}
}

func TestAnalyzeZoomMinimized(t *testing.T) {
	defer func(r func(int) ([]axtree.Element, error)) { readTree = r }(readTree)
	readTree = func(int) ([]axtree.Element, error) {
		return []axtree.Element{
			{Role: "AXWindow", Title: "", Rect: axtree.Rect{X: 1000, Y: 50, Width: 200, Height: 100}},
			{Role: "AXUnknown", Description: "Share content", Rect: axtree.Rect{X: 1000, Y: 50, Width: 200, Height: 100}},
			{Role: "AXWindow", Title: "Zoom Workplace", Rect: axtree.Rect{X: 0, Y: 0, Width: 100, Height: 100}},
		}, nil
	}
	var l Look
	(&Watcher{}).analyzeZoom(&l, zoomFrame(10, 10, 100, 80))
	if l.Stage != StageNoTiles || l.Usable || len(l.Content) == 0 {
		t.Errorf("minimized: stage %s usable %v content %d bytes (%s)", l.Stage, l.Usable, len(l.Content), l.Detail)
	}
}
