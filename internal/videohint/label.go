package videohint

import (
	"fmt"
	"strings"
	"unicode"
)

// LabelRect computes the pixel rectangle of a meeting app's name-label
// overlay within a captured frame, given a matched ring and that
// platform's LabelRegion. Anchored to the ring's bottom-left corner by
// a fixed pixel offset/size (see LabelRegion's doc comment for why
// this is absolute pixels, not a fraction of the ring), and clamped to
// the ring's own width so a small gallery tile doesn't spill the crop
// into a neighboring tile.
func LabelRect(ring RingMatch, label LabelRegion) (x, y, w, h int) {
	x = ring.X
	w = label.MaxWidth
	if w <= 0 || w > ring.Width {
		w = ring.Width
	}
	y = ring.Y + ring.Height - label.BottomOffset
	h = label.Height
	return x, y, w, h
}

// cropRGB copies the (x,y,w,h) rectangle out of a packed RGB frame
// buffer (no padding, 3 bytes per pixel, frameWidth*frameHeight*3
// bytes total) into its own packed RGB buffer. The rectangle is
// clamped to the frame's bounds first, since a ring detected near a
// frame edge can produce a label rectangle that runs slightly past it.
func cropRGB(pix []byte, frameWidth, frameHeight, x, y, w, h int) ([]byte, int, int, error) {
	if frameWidth <= 0 || frameHeight <= 0 || len(pix) < frameWidth*frameHeight*3 {
		return nil, 0, 0, fmt.Errorf("videohint: frame buffer too small for %dx%d RGB", frameWidth, frameHeight)
	}

	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > frameWidth {
		w = frameWidth - x
	}
	if y+h > frameHeight {
		h = frameHeight - y
	}
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("videohint: crop rectangle (%d,%d,%d,%d) is empty after clamping to %dx%d frame", x, y, w, h, frameWidth, frameHeight)
	}

	out := make([]byte, w*h*3)
	for row := 0; row < h; row++ {
		srcStart := ((y+row)*frameWidth + x) * 3
		dstStart := row * w * 3
		copy(out[dstStart:dstStart+w*3], pix[srcStart:srcStart+w*3])
	}
	return out, w, h, nil
}

// RecognizeLabel crops the name-label region implied by a matched ring
// and platform Label config out of frame, then runs OCR on it. Returns
// the recognized text (trimmed of surrounding whitespace is the
// caller's job, since RecognizeText already returns Vision's raw
// output) or an error if cropping or OCR failed.
func RecognizeLabel(pix []byte, frameWidth, frameHeight int, ring RingMatch, label LabelRegion) (string, error) {
	x, y, w, h := LabelRect(ring, label)
	crop, cw, ch, err := cropRGB(pix, frameWidth, frameHeight, x, y, w, h)
	if err != nil {
		return "", err
	}
	text, err := RecognizeText(crop, cw, ch)
	if err != nil {
		return "", err
	}
	return cleanOCRName(text), nil
}

// knownUINoiseWords lists trailing tokens the label crop's own overlay
// chrome can bleed into an OCR read — found live: a real name read as
// "Devin Dobrowolski Priv", where "Priv" came from Teams' background-
// blur/privacy indicator overlapping the crop region, not the name
// itself. Matched case-insensitively as a TRAILING word only (a real
// name is never expected to end with one of these), including a
// partial-word match, since OCR can truncate an overlay icon's own
// label the exact same way it truncates a name.
var knownUINoiseWords = []string{"privacy", "muted", "mute", "recording", "live"}

// cleanOCRName strips a single trailing UI-chrome noise word from a
// raw OCR read of a name label, if the last word matches (or is a
// >=3-character prefix of) one of knownUINoiseWords. Never strips more
// than one trailing word, and leaves a one-word read untouched (there's
// nothing for it to "trail").
func cleanOCRName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	words := strings.Fields(trimmed)
	if len(words) < 2 {
		return trimmed
	}
	last := words[len(words)-1]
	lower := strings.ToLower(last)
	for _, noise := range knownUINoiseWords {
		if lower == noise || (len(lower) >= 3 && strings.HasPrefix(noise, lower)) {
			return strings.Join(words[:len(words)-1], " ")
		}
	}
	// A one- or two-letter lowercase scrap after a name is an icon next to
	// the label read as text (found live: "Amit Tripathi fo" from the
	// co-organizer badge), and so is a token with no letters at all
	// ("Kevin Li •.•"). Names don't end in either.
	if (len(last) <= 2 && last == lower) || !strings.ContainsFunc(last, unicode.IsLetter) {
		return cleanOCRName(strings.Join(words[:len(words)-1], " "))
	}
	return trimmed
}
