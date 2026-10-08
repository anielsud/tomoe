package videohint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/axtree"
	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// Look is one capture-and-detect attempt by a Watcher: what the meeting
// window showed at Time, and what was made of it. Every look is recorded
// (see LookLog) and shown in the app's hint timeline, failures included,
// so what the hint layer does in a real call can be watched as it
// happens.
type Look struct {
	ID     int        `json:"id"`
	Time   time.Time  `json:"time"`
	Stage  EventStage `json:"stage"`
	Detail string     `json:"detail"`
	// Window is the app (and window title) captured.
	Window string `json:"window,omitempty"`
	// WindowID is the window captured, and Pick says why the window rule
	// chose it (automatic mode): what was picked and what was passed over.
	WindowID int    `json:"window_id,omitempty"`
	Pick     string `json:"pick,omitempty"`
	// Windows is every window on screen, front to back, recorded when the
	// set changes and every inventoryEvery (Record for tuning); the
	// pictures are in the session's windows/ folder.
	Windows []teamsvideo.WindowRecord `json:"windows,omitempty"`
	// Ring is the active-speaker ring found, if exactly one was.
	Ring *RingMatch `json:"ring,omitempty"`
	// Rings are every candidate when more than one was found, and
	// Candidates the names read under them ("" where none was): one of
	// them is speaking.
	Rings      []RingMatch `json:"rings,omitempty"`
	Candidates []string    `json:"candidates,omitempty"`
	// Shapes are the measurements of every ring-colored region near the
	// detection thresholds, for tuning them offline.
	Shapes []RingStat `json:"shapes,omitempty"`
	// Tiles are the meeting app's participant tiles as its accessibility
	// tree describes them (Zoom), with how much of each one's border was
	// highlighted; Layout is the view the app says it's showing.
	Tiles  []Tile `json:"tiles,omitempty"`
	Layout string `json:"layout,omitempty"`
	// Cost is what this look took, for measuring the hint layer's CPU
	// use from a recording.
	Cost LookCost `json:"cost"`
	// Name is the active speaker's name: read this look, or remembered
	// from an earlier read of the same tile (FromCache).
	Name      string `json:"name,omitempty"`
	FromCache bool   `json:"from_cache,omitempty"`
	// Usable means Name may be attributed to whoever is speaking at Time:
	// the window showed a live call, exactly one ring was lit, and a name
	// was read for it.
	Usable bool `json:"usable"`
	Width  int  `json:"width,omitempty"`
	Height int  `json:"height,omitempty"`
	// Thumb is a small JPEG of the frame, nil when the frame looked the
	// same as look ThumbOf's (same result a moment earlier).
	Thumb   []byte `json:"-"`
	ThumbOf int    `json:"thumb_of,omitempty"`
	// Content is the shared screen when it changed since the last one
	// kept (a JPEG saved as content/<id>.jpg); ContentRect is where it
	// was in the frame.
	Content     []byte       `json:"-"`
	ContentRect *axtree.Rect `json:"content_rect,omitempty"`
	// Chat is the meeting chat's messages first seen in this look (saved
	// to chat.jsonl, not with the look).
	Chat []ChatMessage `json:"-"`
}

// LookCost is a look's processing time in milliseconds: capturing the
// window, finding rings, reading names (0 when a known tile's name was
// reused), and encoding images.
type LookCost struct {
	Capture float64 `json:"capture_ms"`
	Detect  float64 `json:"detect_ms"`
	OCR     float64 `json:"ocr_ms"`
	Encode  float64 `json:"encode_ms"`
}

// thumbWidth is how wide look thumbnails are, in pixels.
const thumbWidth = 320

// encodeJPEG encodes a packed RGB frame, scaled down to at most maxWidth
// pixels wide (0 = full size), as a JPEG.
func encodeJPEG(pix []byte, width, height, maxWidth, quality int) ([]byte, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return nil, fmt.Errorf("bad frame %dx%d", width, height)
	}
	ow, oh := width, height
	if maxWidth > 0 && width > maxWidth {
		ow, oh = maxWidth, max(1, height*maxWidth/width)
	}
	img := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		sy0, sy1 := y*height/oh, max(y*height/oh+1, (y+1)*height/oh)
		for x := 0; x < ow; x++ {
			sx0, sx1 := x*width/ow, max(x*width/ow+1, (x+1)*width/ow)
			var r, g, b, n int
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					i := (sy*width + sx) * 3
					r, g, b, n = r+int(pix[i]), g+int(pix[i+1]), b+int(pix[i+2]), n+1
				}
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LookLog records a session's looks in dir: one JSON line per look in
// looks.jsonl and each thumbnail as looks/<id>.jpg. It stays on this
// computer, next to the session's audio, and goes when the session is
// deleted.
type LookLog struct {
	dir string
}

// NewLookLog records looks in dir (a session's directory).
func NewLookLog(dir string) *LookLog { return &LookLog{dir: dir} }

// Write appends look.
func (l *LookLog) Write(look Look) error {
	if err := os.MkdirAll(filepath.Join(l.dir, "looks"), 0o755); err != nil {
		return err
	}
	if len(look.Thumb) > 0 {
		if err := os.WriteFile(filepath.Join(l.dir, "looks", fmt.Sprintf("%d.jpg", look.ID)), look.Thumb, 0o644); err != nil {
			return err
		}
	}
	if len(look.Content) > 0 {
		if err := os.MkdirAll(filepath.Join(l.dir, "content"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(l.dir, "content", fmt.Sprintf("%d.jpg", look.ID)), look.Content, 0o644); err != nil {
			return err
		}
	}
	if err := appendJSONLines(filepath.Join(l.dir, "chat.jsonl"), look.Chat); err != nil {
		return err
	}
	line, err := json.Marshal(look)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, "looks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// appendJSONLines appends one JSON line per item to path.
func appendJSONLines[T any](path string, items []T) error {
	if len(items) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, it := range items {
		line, err := json.Marshal(it)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf.Bytes())
	return err
}

// WriteWindowShot saves a picture of one window from look id's inventory
// (Record for tuning) as windows/<look>-<window>[-thumb].jpg.
func (l *LookLog) WriteWindowShot(lookID, windowID int, thumb bool, jpeg []byte) error {
	if err := os.MkdirAll(filepath.Join(l.dir, "windows"), 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d.jpg", lookID, windowID)
	if thumb {
		name = fmt.Sprintf("%d-%d-thumb.jpg", lookID, windowID)
	}
	return os.WriteFile(filepath.Join(l.dir, "windows", name), jpeg, 0o644)
}

// WriteFull saves look id's full-resolution frame (Record for tuning).
func (l *LookLog) WriteFull(id int, jpeg []byte) error {
	if err := os.MkdirAll(filepath.Join(l.dir, "looks"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.dir, "looks", fmt.Sprintf("%d-full.jpg", id)), jpeg, 0o644)
}

// Accepted is a recorded look's name and candidates as today's reading
// would keep them: reads from a ring too short to hold a label, or that
// can't be a name (see plausibleName), are dropped. A name remembered for
// the tile (FromCache) was read earlier and is kept, as the Watcher does.
// For replaying looks recorded before those checks existed.
func (l Look) Accepted() (name string, candidates []string) {
	label := rules[meeting.PlatformTeams].Label
	if l.Usable && plausibleName(l.Name) && (l.FromCache || l.Ring == nil || labelFits(*l.Ring, label)) {
		name = l.Name
	}
	if len(l.Candidates) > 1 {
		candidates = make([]string, len(l.Candidates))
		for i, c := range l.Candidates {
			if plausibleName(c) && (i >= len(l.Rings) || labelFits(l.Rings[i], label)) {
				candidates[i] = c
			}
		}
	}
	return name, candidates
}

// ReadLooks returns the looks recorded in dir (without thumbnails), in
// order; none if there are none.
func ReadLooks(dir string) ([]Look, error) {
	f, err := os.Open(filepath.Join(dir, "looks.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Look
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var l Look
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

// ReadThumb returns look id's thumbnail from dir (following ThumbOf is
// the caller's job).
func ReadThumb(dir string, id int) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, "looks", fmt.Sprintf("%d.jpg", id)))
}

// SaveForAnalysis copies a look into dir/<name>/ for later study: the
// full-resolution frame if there is one (else the thumbnail) and the
// look's details as look.json.
func SaveForAnalysis(dir, name string, look Look, frame []byte) (string, error) {
	out := filepath.Join(dir, name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	if len(frame) > 0 {
		if err := os.WriteFile(filepath.Join(out, "frame.jpg"), frame, 0o644); err != nil {
			return "", err
		}
	}
	js, _ := json.MarshalIndent(look, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "look.json"), js, 0o644); err != nil {
		return "", err
	}
	return out, nil
}
