package videohint

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
)

// WatchConfig configures a Watcher.
type WatchConfig struct {
	// LearnInterval is the time between looks while learning: some
	// speaker still has no confident name (NeedsLearning), or a speaker
	// change just happened (Burst). CheckInterval is the time between
	// looks otherwise, which only confirm names already known.
	LearnInterval time.Duration
	CheckInterval time.Duration
	// NeedsLearning reports whether anyone speaking lacks a confident
	// name. May be nil (always check).
	NeedsLearning func() bool
	// OnLook receives every look, on the Watcher's goroutine.
	OnLook func(Look)
}

// Default look intervals.
const (
	DefaultLearnInterval = 350 * time.Millisecond
	DefaultCheckInterval = time.Second
)

const (
	// burstFor is how long a speaker change keeps the Watcher learning.
	burstFor = 3 * time.Second
	// A tile's name is read again (OCR) once its last read is this old:
	// often while learning, rarely when only confirming.
	rereadLearning = time.Second
	rereadChecking = 5 * time.Second
	// keepFramesFor is how long full-resolution frames are kept in memory
	// for FullFrame (Save for analysis).
	keepFramesFor = 90 * time.Second
	// A look whose result matches the previous one within this long shares
	// its thumbnail.
	sameThumbFor = 10 * time.Second
)

// Watcher watches the meeting window for who's speaking: it captures
// the window, finds the active-speaker ring and reads the name under it,
// often while a speaker still needs naming or right after a speaker
// change, and sparsely otherwise. Ring positions it has read a name for
// are remembered, so a ring returning to a known tile needs no OCR.
type Watcher struct {
	cfg  WatchConfig
	wake chan struct{}

	mu         sync.Mutex
	burstUntil time.Time
	frames     []keptFrame

	// Watcher-goroutine state.
	nextID  int
	last    *Look
	tiles   []tileName
	thumbID int // the last look that has its own thumbnail
	thumbAt time.Time
}

type keptFrame struct {
	id   int
	at   time.Time
	jpeg []byte
}

// tileName is the name last read under a ring at box.
type tileName struct {
	box    RingMatch
	name   string
	readAt time.Time
}

// frame is a captured window, packed RGB.
type frame struct {
	width, height int
	pix           []byte
}

// NewWatcher returns a Watcher; call Run to start it.
func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.LearnInterval <= 0 {
		cfg.LearnInterval = DefaultLearnInterval
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = DefaultCheckInterval
	}
	return &Watcher{cfg: cfg, wake: make(chan struct{}, 1)}
}

// Burst reports a likely speaker change (speech starting after a pause,
// a voice change found by diarization): look now, and keep learning for
// a few seconds. Safe from any goroutine; never blocks.
func (w *Watcher) Burst() {
	w.mu.Lock()
	w.burstUntil = time.Now().Add(burstFor)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// FullFrame returns look id's full-resolution frame as a JPEG while it's
// still kept (looks with their own thumbnail, from about the last 90
// seconds). For a look sharing a thumbnail, ask for its ThumbOf.
func (w *Watcher) FullFrame(id int) ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		if f.id == id {
			return f.jpeg, true
		}
	}
	return nil, false
}

// Run looks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	if !captureSupported {
		<-ctx.Done()
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-w.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		learning := w.learning()
		w.look(learning)
		interval := w.cfg.CheckInterval
		if w.learning() {
			interval = w.cfg.LearnInterval
		}
		timer.Reset(interval)
	}
}

func (w *Watcher) learning() bool {
	w.mu.Lock()
	burst := time.Now().Before(w.burstUntil)
	w.mu.Unlock()
	return burst || w.cfg.NeedsLearning == nil || w.cfg.NeedsLearning()
}

// look captures and analyzes the meeting window once and reports it.
func (w *Watcher) look(learning bool) {
	w.nextID++
	l := Look{ID: w.nextID, Time: time.Now()}
	fr, stage, detail := captureMeetingWindow()
	l.Stage, l.Detail = stage, detail
	if fr != nil {
		l.Width, l.Height = fr.width, fr.height
		w.analyze(&l, fr, learning)
		w.keepThumb(&l, fr)
	}
	w.last = &l
	if w.cfg.OnLook != nil {
		w.cfg.OnLook(l)
	}
}

// analyze finds the ring and the name in fr.
func (w *Watcher) analyze(l *Look, fr *frame, learning bool) {
	rule, ok := ruleFor(meeting.PlatformTeams) // the only window finder so far
	if !ok {
		l.Stage, l.Detail = StageNoRule, "no rule configured for this platform"
		return
	}
	if rule.Chrome.configured() && !DetectCallChrome(fr.pix, fr.width, fr.height, rule.Chrome) {
		// Not a live call (a chat, a recording page): shown, but nothing
		// read here is a name.
		l.Stage, l.Detail = StageNotACall, "no active-call chrome (Leave button not found): likely not a live call"
		return
	}
	rings := DetectRings(fr.pix, fr.width, fr.height, rule.Ring)
	switch len(rings) {
	case 0:
		// Speaker view draws no ring: the main video is the active
		// speaker, named at its bottom left.
		if rule.Label.configured() && speakerView(fr) {
			main := RingMatch{X: stageInset, Y: stageTop, Width: fr.width - 2*stageInset, Height: fr.height - stageTop - stageInset}
			if name, err := RecognizeLabel(fr.pix, fr.width, fr.height, main, rule.Label); err == nil && name != "" {
				l.Stage, l.Name, l.Usable = StageSpeakerView, name, true
				l.Detail = fmt.Sprintf("speaker view: read %q under the main video", name)
				return
			}
		}
		l.Stage, l.Detail = StageNoRingMatch, "no active-speaker ring found"
		return
	case 1:
	default:
		// Teams lights every tile making sound. Read them all: attribution
		// can rule out names that belong to other speakers.
		l.Stage, l.Rings = StageAmbiguousRing, rings
		l.Candidates = make([]string, len(rings))
		reread := rereadChecking
		if learning {
			reread = rereadLearning
		}
		read := 0
		for i, r := range rings {
			if t := w.tileFor(r); t != nil && time.Since(t.readAt) < reread {
				l.Candidates[i] = t.name
			} else if rule.Label.configured() {
				if name, err := RecognizeLabel(fr.pix, fr.width, fr.height, r, rule.Label); err == nil && name != "" {
					l.Candidates[i] = name
					w.setTile(r, name)
				}
			}
			if l.Candidates[i] != "" {
				read++
			}
		}
		l.Detail = fmt.Sprintf("%d tiles lit at once (%d names read): attributing by elimination", len(rings), read)
		return
	}
	ring := rings[0]
	l.Ring = &ring
	if w.last != nil && w.last.Ring != nil && !sameTile(*w.last.Ring, ring) {
		w.Burst() // the ring moved: someone else is speaking
		learning = true
	}
	reread := rereadChecking
	if learning {
		reread = rereadLearning
	}
	if t := w.tileFor(ring); t != nil && time.Since(t.readAt) < reread {
		l.Stage, l.Name, l.FromCache, l.Usable = StageOCRHit, t.name, true, true
		l.Detail = fmt.Sprintf("ring on %s's tile (read %.0fs ago)", t.name, time.Since(t.readAt).Seconds())
		return
	}
	if !rule.Label.configured() {
		l.Stage, l.Detail = StageNoLabelRegion, "ring found but no label region configured"
		return
	}
	name, err := RecognizeLabel(fr.pix, fr.width, fr.height, ring, rule.Label)
	if err != nil || name == "" {
		l.Stage, l.Detail = StageOCRMiss, "ring found but no name read"
		if err != nil {
			l.Detail += ": " + err.Error()
		}
		return
	}
	l.Stage, l.Name, l.Usable = StageOCRHit, name, true
	l.Detail = fmt.Sprintf("read %q under the ring", name)
	w.setTile(ring, name)
}

// keepThumb attaches a thumbnail, or points at the last one if this look
// found the same thing within sameThumbFor, and keeps the full frame for a
// while.
func (w *Watcher) keepThumb(l *Look, fr *frame) {
	if p := w.last; p != nil && w.thumbID != 0 && p.Stage == l.Stage && p.Name == l.Name &&
		sameRing(p.Ring, l.Ring) && l.Time.Sub(w.thumbAt) < sameThumbFor {
		l.ThumbOf = w.thumbID
		return
	}
	if thumb, err := encodeJPEG(fr.pix, fr.width, fr.height, thumbWidth, 70); err == nil {
		l.Thumb = thumb
		w.thumbID, w.thumbAt = l.ID, l.Time
	}
	full, err := encodeJPEG(fr.pix, fr.width, fr.height, 0, 85)
	if err != nil {
		return
	}
	w.mu.Lock()
	w.frames = append(w.frames, keptFrame{id: l.ID, at: l.Time, jpeg: full})
	cut := 0
	for cut < len(w.frames) && l.Time.Sub(w.frames[cut].at) > keepFramesFor {
		cut++
	}
	w.frames = w.frames[cut:]
	w.mu.Unlock()
}

func (w *Watcher) tileFor(r RingMatch) *tileName {
	for i := range w.tiles {
		if sameTile(w.tiles[i].box, r) {
			return &w.tiles[i]
		}
	}
	return nil
}

func (w *Watcher) setTile(r RingMatch, name string) {
	if t := w.tileFor(r); t != nil {
		t.box, t.name, t.readAt = r, name, time.Now()
		return
	}
	w.tiles = append(w.tiles, tileName{box: r, name: name, readAt: time.Now()})
}

// sameTile reports whether two rings outline the same tile: centers
// within a few pixels and sizes within 10%.
func sameTile(a, b RingMatch) bool {
	near := func(x, y, tol int) bool { return x-y <= tol && y-x <= tol }
	return near(a.X+a.Width/2, b.X+b.Width/2, 8) && near(a.Y+a.Height/2, b.Y+b.Height/2, 8) &&
		near(a.Width, b.Width, max(4, a.Width/10)) && near(a.Height, b.Height, max(4, a.Height/10))
}

func sameRing(a, b *RingMatch) bool {
	if a == nil || b == nil {
		return a == b
	}
	return sameTile(*a, *b)
}

// The video area starts below Teams' toolbar; stageInset is its margin.
const (
	stageTop   = 96
	stageInset = 5
)

// speakerView reports whether fr looks like Teams' speaker view (one video
// filling the stage) rather than a gallery: a gallery leaves a margin of
// Teams' flat dark stage background (brightness 29, measured on saved
// frames) at the stage's left edge, while in speaker view the video runs
// to it, whatever it shows.
func speakerView(fr *frame) bool {
	x0, x1 := stageInset+3, stageInset+35
	y0, y1 := stageTop+30, fr.height-80
	if x1 >= fr.width || y1 <= y0 {
		return false
	}
	var sum, sq float64
	n := 0
	for y := y0; y < y1; y += 2 {
		for x := x0; x < x1; x += 2 {
			i := (y*fr.width + x) * 3
			v := 0.299*float64(fr.pix[i]) + 0.587*float64(fr.pix[i+1]) + 0.114*float64(fr.pix[i+2])
			sum += v
			sq += v * v
			n++
		}
	}
	mean := sum / float64(n)
	flat := sq/float64(n)-mean*mean < 4
	stageDark := mean > stageBackground-8 && mean < stageBackground+8
	return !(flat && stageDark)
}

// stageBackground is the brightness of Teams' dark stage background.
const stageBackground = 29.0
