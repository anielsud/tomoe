package videohint

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/axtree"
)

// Zoom describes every participant tile in its accessibility tree ("Alex
// Kim, Computer audio unmuted, Video on") with the tile's box on screen,
// and draws a green border round the active speaker's tile. So a look
// needs no text recognition: find the tile whose border is green in the
// frame and take its name from the tree. Speaker view shows one tile, the
// active speaker's.

// Tile is one participant tile.
type Tile struct {
	Name    string      `json:"name"`
	Unmuted bool        `json:"unmuted,omitempty"`
	Video   bool        `json:"video,omitempty"`
	Rect    axtree.Rect `json:"rect"`
	// Highlight is the fraction of the tile's border that is Zoom's
	// active-speaker green.
	Highlight float64 `json:"highlight"`
}

// zoomTileDesc matches a tile's description: "<name>, Computer audio
// unmuted, Video on", or "<name>, No audio connected" (a room or a
// spotlighted video without an audio link).
var zoomTileDesc = regexp.MustCompile(`^(.+?), (?:(?:Computer|Telephone|Phone) audio (muted|unmuted)|No audio connected)(?:, Video (on|off))?`)

// zoomChatDesc matches a chat message: "<sender>, <text>, 10:05 AM" (Zoom
// puts a narrow no-break space before AM/PM).
var zoomChatDesc = regexp.MustCompile(`(?s)^(.+?), (.+), (\d{1,2}:\d{2}[\s\x{202F}][AP]M)$`)

// zoomShareDesc is the element showing a shared screen.
const zoomShareDesc = "Share content"

// ChatMessage is one message of the meeting chat as the app shows it.
type ChatMessage struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	// At is the time the app shows (its own clock, minutes only); Seen is
	// when it was first read, and Look the look that read it.
	At   string    `json:"at"`
	Seen time.Time `json:"seen"`
	Look int       `json:"look"`
}

// zoomState is what the watcher remembers between Zoom looks.
type zoomState struct {
	chatSeen   map[string]bool
	contentSig []uint8
	contentAt  time.Time
}

const (
	// contentDiff is how different (mean brightness, 0-255) the shared
	// screen has to be from the last one kept to keep it: a new slide,
	// not a moving pointer.
	contentDiff = 6.0
	// contentGap is the least time between kept screens, so shared video
	// or a scrolling page doesn't save a picture every look.
	contentGap = 3 * time.Second
	// contentMaxWidth bounds a kept screen's width in pixels.
	contentMaxWidth = 1600
)

// zoomMeetingWindow is the title of Zoom's call window (its other window,
// "Zoom Workplace", is the home screen).
const zoomMeetingWindow = "Zoom Meeting"

// zoomTiles finds the call window and its tiles among elems (in tree
// order, each window followed by its contents).
func zoomTiles(elems []axtree.Element) (win axtree.Rect, tiles []Tile, layout string, ok bool) {
	win, tiles, layout, _, _, ok = zoomWindow(elems)
	return win, tiles, layout, ok
}

// zoomWindow reads everything a look uses from the call window: its box,
// tiles, layout, chat messages (as visible in the chat panel) and the
// shared screen's box (nil when nothing is shared).
func zoomWindow(elems []axtree.Element) (win axtree.Rect, tiles []Tile, layout string, chat []ChatMessage, share *axtree.Rect, ok bool) {
	// While the call window is minimized it isn't in the tree; an
	// untitled window holding only the shared screen is.
	want := zoomMeetingWindow
	if !hasZoomWindow(elems, zoomMeetingWindow) {
		want = ""
	}
	in := false
	for _, e := range elems {
		if e.Role == "AXWindow" {
			in = !ok && e.Title == want
			if in {
				win, ok = e.Rect, true
			}
			continue
		}
		if !in {
			continue
		}
		if v, found := strings.CutPrefix(e.Description, "View options, "); found {
			layout = v
			continue
		}
		if e.Description == zoomShareDesc && e.Rect.Width > 0 && e.Rect.Height > 0 {
			r := e.Rect
			share = &r
			continue
		}
		if m := zoomTileDesc.FindStringSubmatch(e.Description); m != nil && e.Rect.Width > 0 && e.Rect.Height > 0 {
			tiles = append(tiles, Tile{Name: strings.TrimSpace(m[1]), Unmuted: m[2] == "unmuted", Video: m[3] == "on", Rect: e.Rect})
			continue
		}
		if e.Role == "AXUnknown" {
			if m := zoomChatDesc.FindStringSubmatch(e.Description); m != nil {
				chat = append(chat, ChatMessage{Sender: strings.TrimSpace(m[1]), Text: m[2], At: strings.ReplaceAll(m[3], "\u202f", " ")})
			}
		}
	}
	return win, tiles, layout, chat, share, ok
}

// hasZoomWindow reports whether elems include a window titled title.
func hasZoomWindow(elems []axtree.Element, title string) bool {
	for _, e := range elems {
		if e.Role == "AXWindow" && e.Title == title {
			return true
		}
	}
	return false
}

// toFrame maps a box in screen points to frame pixels, clamped to the
// frame (the frame is the window win).
func toFrame(r, win axtree.Rect, width, height int) (x0, y0, x1, y1 int) {
	sx, sy := float64(width)/win.Width, float64(height)/win.Height
	x0, y0 = int((r.X-win.X)*sx), int((r.Y-win.Y)*sy)
	x1, y1 = int((r.X+r.Width-win.X)*sx), int((r.Y+r.Height-win.Y)*sy)
	return max(0, x0), max(0, y0), min(width, x1), min(height, y1)
}

// crop copies the box (x0,y0)-(x1,y1) out of a packed RGB frame.
func crop(pix []byte, width, x0, y0, x1, y1 int) []byte {
	w := x1 - x0
	out := make([]byte, 0, w*(y1-y0)*3)
	for y := y0; y < y1; y++ {
		i := (y*width + x0) * 3
		out = append(out, pix[i:i+w*3]...)
	}
	return out
}

// zoomExtras records the chat messages not seen before and, when the
// shared screen changed enough, a picture of it.
func (w *Watcher) zoomExtras(l *Look, fr *frame, win axtree.Rect, chat []ChatMessage, share *axtree.Rect) {
	if w.zoom.chatSeen == nil {
		w.zoom.chatSeen = map[string]bool{}
	}
	for _, m := range chat {
		key := m.Sender + "\x00" + m.Text + "\x00" + m.At
		if w.zoom.chatSeen[key] {
			continue
		}
		w.zoom.chatSeen[key] = true
		m.Seen, m.Look = l.Time, l.ID
		l.Chat = append(l.Chat, m)
	}
	if share == nil || win.Width <= 0 || win.Height <= 0 {
		return
	}
	x0, y0, x1, y1 := toFrame(*share, win, fr.width, fr.height)
	if x1-x0 < 50 || y1-y0 < 50 {
		return
	}
	pix := crop(fr.pix, fr.width, x0, y0, x1, y1)
	sig := lumaSig(pix, x1-x0, y1-y0)
	if w.zoom.contentSig != nil && (l.Time.Sub(w.zoom.contentAt) < contentGap || sigDiff(w.zoom.contentSig, sig) <= contentDiff) {
		return
	}
	jpg, err := encodeJPEG(pix, x1-x0, y1-y0, contentMaxWidth, 80)
	if err != nil {
		return
	}
	w.zoom.contentSig, w.zoom.contentAt = sig, l.Time
	r := *share
	l.Content, l.ContentRect = jpg, &r
}

// zoomHighlightMin is how much of a tile's border has to be green to be
// the active speaker's (measured: 0.33 on the highlighted tile, 0 on the
// others).
const zoomHighlightMin = 0.15

// zoomGreen reports whether an RGB pixel is Zoom's active-speaker border
// (about 138,200,105 after the capture's smoothing).
func zoomGreen(r, g, b byte) bool {
	return g > 140 && int(g)-int(r) > 50 && int(g)-int(b) > 30
}

// borderHighlight is the fraction of pixels within 3 px of the box's
// edges (in frame pixels) that are Zoom's border green.
func borderHighlight(pix []byte, width, height, x0, y0, x1, y1 int) float64 {
	const pad = 3
	var n, hit int
	count := func(x, y int) {
		if x < 0 || y < 0 || x >= width || y >= height {
			return
		}
		i := (y*width + x) * 3
		n++
		if zoomGreen(pix[i], pix[i+1], pix[i+2]) {
			hit++
		}
	}
	for x := x0; x < x1; x++ {
		for d := -pad; d < pad; d++ {
			count(x, y0+d)
			count(x, y1+d)
		}
	}
	for y := y0; y < y1; y++ {
		for d := -pad; d < pad; d++ {
			count(x0+d, y)
			count(x1+d, y)
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}

// readTree is axtree.Read, replaceable in tests.
var readTree = axtree.Read

// analyzeZoom names the active speaker in a Zoom frame from the app's
// accessibility tree and the highlighted tile.
func (w *Watcher) analyzeZoom(l *Look, fr *frame) {
	elems, err := readTree(fr.pid)
	if err != nil {
		l.Stage, l.Detail = StageNoTiles, "couldn't read Zoom's accessibility tree: "+err.Error()
		return
	}
	win, tiles, layout, chat, share, ok := zoomWindow(elems)
	l.Layout = layout
	if ok {
		w.zoomExtras(l, fr, win, chat, share)
	}
	if !ok || len(tiles) == 0 || win.Width <= 0 {
		l.Stage, l.Detail = StageNoTiles, "no participant tiles in Zoom's call window"
		if ok && share != nil && len(tiles) == 0 {
			l.Detail = "Zoom's call window is minimized: only the shared screen is shown, so no speaker or chat"
		}
		return
	}
	// The frame is the window: map screen points to its pixels.
	best := -1
	for i := range tiles {
		x0, y0, x1, y1 := toFrame(tiles[i].Rect, win, fr.width, fr.height)
		tiles[i].Highlight = borderHighlight(fr.pix, fr.width, fr.height, x0, y0, x1, y1)
		if tiles[i].Highlight >= zoomHighlightMin && (best < 0 || tiles[i].Highlight > tiles[best].Highlight) {
			best = i
		}
	}
	l.Tiles = tiles
	switch {
	case best >= 0:
		l.Stage, l.Name, l.Usable = StageTileHighlight, tiles[best].Name, true
		l.Detail = fmt.Sprintf("%s's tile is highlighted (%.0f%% of its border; %d tiles)", tiles[best].Name, 100*tiles[best].Highlight, len(tiles))
	case len(tiles) == 1:
		// Speaker view: the one tile shown is whoever is speaking.
		l.Stage, l.Name, l.Usable = StageSpeakerView, tiles[0].Name, true
		l.Detail = fmt.Sprintf("speaker view: %s", tiles[0].Name)
	default:
		l.Stage, l.Detail = StageNoRingMatch, fmt.Sprintf("no highlighted tile among %d", len(tiles))
	}
}
