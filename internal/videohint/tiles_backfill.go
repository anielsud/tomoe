package videohint

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"
)

// minBackfillTile is the narrowest a tile may be in the saved picture
// for a crop to be worth keeping (pixels): a thumbnail's tile narrower
// than this is a blur.
const minBackfillTile = 80

// BackfillTiles keeps tile crops for a saved session's looks from before
// they were kept at the time (see Look.TileCrop), from the pictures it
// saved: the full frame where there is one (Record for tuning), else the
// thumbnail if the tile in it is wide enough. Like the Watcher, at most
// one a minute per name; looks that already have one are left alone.
// Returns how many it kept.
func BackfillTiles(dir string) (int, error) {
	looks, err := ReadLooks(dir)
	if err != nil {
		return 0, err
	}
	last := map[string]time.Time{}
	kept := 0
	for _, l := range looks {
		if l.Name == "" || !l.Usable || l.Ring == nil || l.Width == 0 {
			continue
		}
		tilePath := filepath.Join(dir, "looks", fmt.Sprintf("%d-tile.jpg", l.ID))
		if l.HasTile || fileExists(tilePath) {
			last[l.Name] = l.Time
			continue
		}
		if at, ok := last[l.Name]; ok && l.Time.Sub(at) < tileEvery {
			continue
		}
		img, scale := backfillSource(dir, l)
		if img == nil {
			continue
		}
		r := l.Ring
		box := image.Rect(int(float64(r.X)*scale), int(float64(r.Y)*scale), int(float64(r.X+r.Width)*scale), int(float64(r.Y+r.Height)*scale))
		if box.Dx() < minBackfillTile {
			continue
		}
		jpg, err := cropTile(img, box)
		if err != nil {
			continue
		}
		if err := os.WriteFile(tilePath, jpg, 0o644); err != nil {
			return kept, err
		}
		last[l.Name] = l.Time
		kept++
	}
	return kept, nil
}

// backfillSource is the saved picture of look l and its scale to l's
// frame: the full frame (1), else the thumbnail.
func backfillSource(dir string, l Look) (image.Image, float64) {
	if img := readJPEG(filepath.Join(dir, "looks", fmt.Sprintf("%d-full.jpg", l.ID))); img != nil {
		return img, float64(img.Bounds().Dx()) / float64(l.Width)
	}
	id := l.ID
	if l.ThumbOf != 0 {
		id = l.ThumbOf
	}
	if img := readJPEG(filepath.Join(dir, "looks", fmt.Sprintf("%d.jpg", id))); img != nil {
		return img, float64(img.Bounds().Dx()) / float64(l.Width)
	}
	return nil, 0
}

func readJPEG(path string) image.Image {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return img
}

// cropTile crops box (with keepTile's thin margin) out of img as a
// tileWidth-wide JPEG.
func cropTile(img image.Image, box image.Rectangle) ([]byte, error) {
	pad := max(4, min(box.Dx(), box.Dy())/20)
	b := img.Bounds()
	r := image.Rect(box.Min.X-pad, box.Min.Y-pad, box.Max.X+pad, box.Max.Y+pad).Intersect(b)
	if r.Dx() < 8 || r.Dy() < 8 {
		return nil, fmt.Errorf("tile too small")
	}
	pix := make([]byte, 0, r.Dx()*r.Dy()*3)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			pix = append(pix, byte(cr>>8), byte(cg>>8), byte(cb>>8))
		}
	}
	return encodeJPEG(pix, r.Dx(), r.Dy(), tileWidth, 80)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// HasTileFile reports whether look id in dir has a kept tile crop (kept
// at the time, or backfilled).
func HasTileFile(dir string, id int) bool {
	return fileExists(filepath.Join(dir, "looks", fmt.Sprintf("%d-tile.jpg", id)))
}
