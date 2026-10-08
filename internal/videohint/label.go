package videohint

import (
	"errors"
	"fmt"
	"regexp"
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
// and platform Label config out of frame and reads the name in it (the
// widest line of text there; see recognizeName), cleaned of UI noise.
func RecognizeLabel(pix []byte, frameWidth, frameHeight int, ring RingMatch, label LabelRegion) (string, error) {
	if !labelFits(ring, label) {
		return "", errRingTooShort
	}
	x, y, w, h := LabelRect(ring, label)
	crop, cw, ch, err := cropRGB(pix, frameWidth, frameHeight, x, y, w, h)
	if err != nil {
		return "", err
	}
	text, err := recognizeName(crop, cw, ch)
	if err != nil {
		return "", err
	}
	name := cleanOCRName(text)
	if !plausibleName(name) {
		return "", nil
	}
	return name, nil
}

// errRingTooShort: the ring is shorter than the label's offset from its
// bottom edge, so the label box would sit above the tile. Found live: a
// Teams popup covered most of the lit tile, the ring was found as a
// 129x6 px strip, and the box above it read the call toolbar ("Take
// control | Annotate") as the speaker's name for a whole talk.
var errRingTooShort = errors.New("ring too short to hold a name label (tile covered?)")

// labelFits reports whether ring is tall enough for label's box to start
// inside it.
func labelFits(ring RingMatch, label LabelRegion) bool {
	return ring.Height >= label.BottomOffset
}

// meetingToolbarWords are the Teams call toolbar's button labels. A read
// mostly made of them is the toolbar, not a name label (see
// errRingTooShort).
var meetingToolbarWords = map[string]bool{
	"take": true, "control": true, "annotate": true, "pop": true, "out": true,
	"chat": true, "people": true, "raise": true, "react": true, "view": true,
	"notes": true, "apps": true, "more": true, "camera": true, "mic": true,
	"share": true, "leave": true,
}

// signalBars is Teams' connection-strength icon read as text ("ill",
// ".ll"): on the self-view, where there's no name, it's all the label
// strip holds. barsPrefix is the same icon in front of a name or badge
// ("lAlex Kim", "il Alex Kim", ".IlVoice isolation"): glyphs ending in
// a lowercase one, then an optional space and a capital, so a name that
// starts with I ("Imogen") is left alone.
var (
	signalBars = regexp.MustCompile(`^[.|lIi1!:']+$`)
	barsPrefix = regexp.MustCompile(`^[.|lIi1!:']*[.|li1!:'] ?(\p{Lu})`)
)

// teamsBadges are status pills Teams shows in the label strip instead of
// (or beside) a name, read whole.
var teamsBadges = map[string]bool{"voice isolation": true, "noise suppressed": true, "noise suppression": true}

// plausibleName rejects reads that can't be a person's name: fewer than
// two letters ("E", "-", seen when a crop catches an icon), the signal
// bars icon alone or an audio badge (see signalBars, teamsBadges), or two or more
// words of which most are toolbar labels (a first word cut short, "ake",
// still counts as the toolbar's other words outnumber it).
func plausibleName(name string) bool {
	if signalBars.MatchString(name) || teamsBadges[strings.ToLower(strings.TrimSpace(name))] {
		return false
	}
	letters := 0
	for _, r := range name {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 2 {
		return false
	}
	words := strings.Fields(strings.ToLower(name))
	ui := 0
	for _, w := range words {
		if meetingToolbarWords[strings.Trim(w, ".,|…")] {
			ui++
		}
	}
	return ui < 2 || 2*ui <= len(words)
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
	trimmed := barsPrefix.ReplaceAllString(strings.TrimSpace(raw), "$1")
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
	// the label read as text (found live: "Priya Desai fo" from the
	// co-organizer badge), and so is a token with no letters at all
	// ("Alex Kim •.•"). Names don't end in either.
	if (len(last) <= 2 && last == lower) || !strings.ContainsFunc(last, unicode.IsLetter) {
		return cleanOCRName(strings.Join(words[:len(words)-1], " "))
	}
	return trimmed
}
