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
