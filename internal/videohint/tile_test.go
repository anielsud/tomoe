package videohint

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"
)

func TestKeepTile(t *testing.T) {
	const w, h = 400, 300
	fr := &frame{width: w, height: h, pix: make([]byte, w*h*3)}
	w0 := &Watcher{}
	now := time.Now()
	l := Look{Time: now, Name: "Ilse Novak", Usable: true, tileRect: image.Rect(100, 100, 200, 180)}
	w0.keepTile(&l, fr)
	if !l.HasTile || len(l.TileCrop) == 0 {
		t.Fatal("no tile kept")
	}
	img, err := jpeg.Decode(bytes.NewReader(l.TileCrop))
	if err != nil {
		t.Fatal(err)
	}
	// 100x80 plus a thin margin (4 px) each side, under tileWidth.
	if b := img.Bounds(); b.Dx() != 108 || b.Dy() != 88 {
		t.Errorf("crop is %v, want 108x88", b)
	}
	again := Look{Time: now.Add(10 * time.Second), Name: "Ilse Novak", Usable: true, tileRect: image.Rect(100, 100, 200, 180)}
	w0.keepTile(&again, fr)
	if again.HasTile {
		t.Error("a name's tile was kept twice within a minute")
	}
	other := Look{Time: now.Add(10 * time.Second), Name: "Wayde Tyrrel", Usable: true, tileRect: image.Rect(350, 250, 420, 320)}
	w0.keepTile(&other, fr)
	if !other.HasTile {
		t.Error("another name's tile, at the frame's edge, wasn't kept")
	}
	none := Look{Time: now, Name: "Bodil Ekdahl", Usable: false, tileRect: image.Rect(0, 0, 50, 50)}
	w0.keepTile(&none, fr)
	if none.HasTile {
		t.Error("a look that names nobody kept a tile")
	}
}

func TestBackfillTiles(t *testing.T) {
	dir := t.TempDir()
	log := NewLookLog(dir)
	// A 1600x1200 frame: its 320-wide thumbnail shows a 500-wide tile at
	// 100 px (kept) and a 200-wide one at 40 (a blur: skipped).
	thumb, err := encodeJPEG(make([]byte, 1600*1200*3), 1600, 1200, thumbWidth, 70)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	looks := []Look{
		{ID: 1, Time: at, Name: "Ilse Novak", Usable: true, Ring: &RingMatch{X: 100, Y: 100, Width: 500, Height: 300}, Width: 1600, Height: 1200, Thumb: thumb},
		{ID: 2, Time: at.Add(5 * time.Second), Name: "Ilse Novak", Usable: true, Ring: &RingMatch{X: 100, Y: 100, Width: 500, Height: 300}, Width: 1600, Height: 1200, ThumbOf: 1},
		{ID: 3, Time: at.Add(70 * time.Second), Name: "Wayde Tyrrel", Usable: true, Ring: &RingMatch{X: 100, Y: 600, Width: 200, Height: 200}, Width: 1600, Height: 1200, ThumbOf: 1},
		{ID: 4, Time: at.Add(80 * time.Second), Name: "Ilse Novak", Usable: true, Ring: &RingMatch{X: 100, Y: 100, Width: 500, Height: 300}, Width: 1600, Height: 1200, ThumbOf: 1},
	}
	for _, l := range looks {
		if err := log.Write(l); err != nil {
			t.Fatal(err)
		}
	}
	n, err := BackfillTiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || !HasTileFile(dir, 1) || HasTileFile(dir, 2) || HasTileFile(dir, 3) || !HasTileFile(dir, 4) {
		t.Errorf("kept %d: want looks 1 and 4 (a minute apart), not 2 (too soon) or 3 (too small)", n)
	}
	if n, _ := BackfillTiles(dir); n != 0 {
		t.Errorf("a second run kept %d more", n)
	}
}
