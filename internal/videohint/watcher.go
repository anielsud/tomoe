package videohint

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// Video hint sources (WatchConfig.Source): SourceAuto finds the Teams
// meeting window; SourceNone turns hints off; anything else is an app's
// name, whose largest window is watched. An app with no rule yet (Teams
// and Zoom have one) is still captured, so its frames can be saved for
// analysis and a rule written from them; its looks never name anyone.
const (
	SourceAuto = ""
	SourceNone = "none"
)

// WindowChoice is an app whose window video hints could watch.
type WindowChoice struct {
	App   string `json:"app"`
	Title string `json:"title"`
	// Known is whether there's a rule for reading its active speaker.
	Known bool `json:"known"`
}

// WatchConfig configures a Watcher.
type WatchConfig struct {
	// Source is which window to watch (see SourceAuto); SetSource changes
	// it while running.
	Source string
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
	// OnFullFrame, if set, receives the full-resolution JPEG of every look
	// with its own thumbnail (Record for tuning), on the Watcher's
	// goroutine.
	OnFullFrame func(id int, jpeg []byte)
	// OnWindowShot, if set (Record for tuning), switches on the window
	// inventory: every look lists the windows on screen, and when the set
	// changes (and every inventoryEvery) it also gets a picture of each
	// Teams window (full size) and a thumbnail of the others. Called on
	// the Watcher's goroutine.
	OnWindowShot func(lookID, windowID int, thumb bool, jpeg []byte)
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
	// inventoryEvery is how often the window inventory is recorded again
	// even if nothing changed; maxInventoryShots caps the pictures taken
	// of other apps' windows each time.
	// keepDiff is how different (mean brightness change per cell, 0-255) a
	// window must look from the last picture kept of it to be kept again;
	// keepGap is the least time between pictures kept on that account. A
	// layout change moves most cells a lot; people moving in their tiles,
	// or a ring jumping, barely does.
	keepDiff = 12.0
	keepGap  = time.Second
	// shotCheckEvery is how often the other Teams windows are looked at.
	shotCheckEvery    = time.Second
	inventoryEvery    = 30 * time.Second
	inventoryMinGap   = 5 * time.Second
	maxInventoryShots = 12
)

// Watcher watches the meeting window for who's speaking: it captures
// the window, finds the active-speaker ring and reads the name under it,
// often while a speaker still needs naming or right after a speaker
// change, and sparsely otherwise. Ring positions it has read a name for
// are remembered, so a ring returning to a known tile needs no OCR.
type Watcher struct {
	cfg  WatchConfig
	wake chan struct{}

	mu            sync.Mutex
	source        string
	sourceChanged bool // the watcher goroutine forgets its tiles
	burstUntil    time.Time
	frames        []keptFrame

	// Watcher-goroutine state.
	nextID      int
	last        *Look
	tiles       []tileName
	thumbID     int // the last look that has its own thumbnail
	thumbAt     time.Time
	thumbSig    []uint8 // the picture last kept of the watched window
	shots       map[int]*shotState
	shotCheckAt time.Time
	ui          uiTracker // is the window repainting its interface?
	call        callCheck // is the window a 1:1 call? (cached)
	invKey      string    // the last window list recorded
	invListAt   time.Time
	invTeamsKey string // the Teams windows when last pictured
	invAt       time.Time
	zoom        zoomState // chat already seen, shared screen last kept
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

// frame is a captured window, packed RGB. windowID and pick say which
// window it is and why it was chosen (automatic mode).
type frame struct {
	width, height int
	pix           []byte
	windowID      int
	pick          string
	scale         int // pixels per point of the capture (1 when unknown)
	pid           int // the window's app, for reading its accessibility tree
}

// NewWatcher returns a Watcher; call Run to start it.
func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.LearnInterval <= 0 {
		cfg.LearnInterval = DefaultLearnInterval
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = DefaultCheckInterval
	}
	return &Watcher{cfg: cfg, source: cfg.Source, wake: make(chan struct{}, 1)}
}

// SetSource switches the window watched (see SourceAuto). Safe from any
// goroutine.
func (w *Watcher) SetSource(source string) {
	w.mu.Lock()
	w.source, w.sourceChanged = source, true
	w.mu.Unlock()
	w.Burst()
}

// SetOnWindowShot sets WatchConfig.OnWindowShot; call before Run.
func (w *Watcher) SetOnWindowShot(f func(lookID, windowID int, thumb bool, jpeg []byte)) {
	w.cfg.OnWindowShot = f
}

// SetOnFullFrame sets WatchConfig.OnFullFrame; call before Run.
func (w *Watcher) SetOnFullFrame(f func(id int, jpeg []byte)) { w.cfg.OnFullFrame = f }

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
	w.mu.Lock()
	source := w.source
	if w.sourceChanged {
		w.sourceChanged, w.tiles, w.last = false, nil, nil
		w.zoom = zoomState{}
	}
	w.mu.Unlock()
	fr, platform, window, stage, detail := captureWindow(source)
	l.Cost.Capture = msSince(l.Time)
	l.Stage, l.Detail, l.Window = stage, detail, window
	if fr != nil {
		l.WindowID = fr.windowID
	}
	if w.cfg.OnWindowShot != nil {
		w.inventory(&l)
	}
	if fr != nil {
		l.Width, l.Height, l.WindowID, l.Pick = fr.width, fr.height, fr.windowID, fr.pick
		w.analyze(&l, fr, platform, learning)
		began := time.Now()
		w.keepThumb(&l, fr)
		l.Cost.Encode = msSince(began)
	}
	w.last = &l
	if w.cfg.OnLook != nil {
		w.cfg.OnLook(l)
	}
}

// analyze finds the ring and the name in fr.
func (w *Watcher) analyze(l *Look, fr *frame, platform meeting.Platform, learning bool) {
	if isBlank(fr.pix, fr.width, fr.height) {
		l.Stage = StageBlankCapture
		l.Detail = fmt.Sprintf("captured %dx%d but every pixel is black: the window can't be read this way (its sharing state is in the window list)", fr.width, fr.height)
		return
	}
	if platform == meeting.PlatformZoom {
		w.analyzeZoom(l, fr)
		return
	}
	rule, ok := ruleFor(platform)
	if !ok {
		l.Stage, l.Detail = StageNoRule, "no rule for this app yet: frames are kept for analysis, nothing is read"
		return
	}
	if rule.Chrome.configured() && !DetectCallChrome(fr.pix, fr.width, fr.height, rule.Chrome) {
		// Not a live call (a chat, a recording page): shown, but nothing
		// read here is a name.
		l.Stage, l.Detail = StageNotACall, "no active-call chrome (Leave button not found): likely not a live call"
		return
	}
	if frozen := w.ui.update(fr.pix, fr.width, fr.height, fr.scale, l.Time); frozen >= uiFrozenAfter {
		// Teams stops repainting a window that's hidden or in the
		// background (only the video tiles keep moving): the timer, the
		// speaker highlight and the name labels stay as they were, so
		// whatever they say is stale, not who is speaking now.
		l.Stage = StageUIFrozen
		l.Detail = fmt.Sprintf("the window's interface hasn't repainted for %.0f s (the call timer is unchanged): Teams doesn't update a hidden window, so its speaker highlight is stale", frozen.Seconds())
		return
	}
	began := time.Now()
	rings, shapes := DetectRingsWithStats(fr.pix, fr.width, fr.height, rule.Ring)
	l.Shapes = shapes
	l.Cost.Detect = msSince(began)
	ocrBegan := time.Now()
	defer func() {
		if !l.FromCache {
			l.Cost.OCR = msSince(ocrBegan)
		}
	}()
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
		// A 1:1 call has no ring either, and only one other person.
		if w.oneOnOne(l, fr, rule) {
			return
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
	sig := lumaSig(fr.pix, fr.width, fr.height)
	if p := w.last; p != nil && w.thumbID != 0 && p.Stage == l.Stage && p.Name == l.Name &&
		sameRing(p.Ring, l.Ring) && p.Width == l.Width && p.Height == l.Height && l.Time.Sub(w.thumbAt) < sameThumbFor &&
		// Recording for tuning keeps every picture that looks different,
		// whatever was found in it (a new view with no ring is the point).
		!(w.cfg.OnWindowShot != nil && l.Time.Sub(w.thumbAt) >= keepGap && sigDiff(w.thumbSig, sig) > keepDiff) {
		l.ThumbOf = w.thumbID
		return
	}
	w.thumbSig = sig
	if thumb, err := encodeJPEG(fr.pix, fr.width, fr.height, thumbWidth, 70); err == nil {
		l.Thumb = thumb
		w.thumbID, w.thumbAt = l.ID, l.Time
	}
	full, err := encodeJPEG(fr.pix, fr.width, fr.height, 0, 85)
	if err != nil {
		return
	}
	if w.cfg.OnFullFrame != nil {
		w.cfg.OnFullFrame(l.ID, full)
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

// inventory lists the windows on screen on l and pictures them.
//
// The list is recorded when the set of windows changed (at most every
// inventoryMinGap, since other apps' windows come and go constantly) or
// inventoryEvery has passed.
//
// Teams windows other than the watched one (which its looks already
// keep) are looked at every shotCheckEvery and a full-size picture kept
// whenever it's the first of that window, its size changed, it looks
// different (keepDiff, at most one per keepGap), or inventoryEvery has
// passed. Other apps' windows get a thumbnail whenever the Teams windows
// change or inventoryEvery passes.
func (w *Watcher) inventory(l *Look) {
	recs := snapshotWindows()
	var all, teamsKey strings.Builder
	rank := 0
	for _, r := range recs {
		fmt.Fprintf(&all, "%d|%s|%s|%dx%d|%d;", r.ID, r.Owner, r.Title, r.Width, r.Height, r.Layer)
		if r.IsTeams() {
			fmt.Fprintf(&teamsKey, "%d|%s|%dx%d|%d;", r.ID, r.Title, r.Width, r.Height, rank)
			rank++
		}
	}
	beat := l.Time.Sub(w.invAt) >= inventoryEvery
	if all.String() != w.invKey && l.Time.Sub(w.invListAt) >= inventoryMinGap || beat {
		w.invKey, w.invListAt = all.String(), l.Time
		l.Windows = recs
	}
	teamsChanged := teamsKey.String() != w.invTeamsKey
	if teamsChanged || beat {
		w.invTeamsKey, w.invAt = teamsKey.String(), l.Time
		w.pictureOthers(l, recs)
	}
	if l.Time.Sub(w.shotCheckAt) < shotCheckEvery && !teamsChanged && !beat {
		return
	}
	w.shotCheckAt = l.Time
	if w.shots == nil {
		w.shots = map[int]*shotState{}
	}
	for _, r := range recs {
		if !r.IsTeams() || r.Width < 100 || r.Height < 60 || r.ID == l.WindowID && l.WindowID != 0 {
			continue
		}
		fr, err := captureWindowByID(r.ID)
		if err != nil || fr == nil {
			continue
		}
		sig := lumaSig(fr.pix, fr.width, fr.height)
		st := w.shots[r.ID]
		keep := st == nil || st.w != fr.width || st.h != fr.height || l.Time.Sub(st.at) >= inventoryEvery ||
			(l.Time.Sub(st.at) >= keepGap && sigDiff(st.sig, sig) > keepDiff)
		if !keep {
			continue
		}
		if jpegBytes, err := encodeJPEG(fr.pix, fr.width, fr.height, 0, 80); err == nil {
			w.cfg.OnWindowShot(l.ID, r.ID, false, jpegBytes)
			w.shots[r.ID] = &shotState{sig: sig, w: fr.width, h: fr.height, at: l.Time}
		}
	}
}

// shotState is the last full picture kept of a Teams window.
type shotState struct {
	sig  []uint8
	w, h int
	at   time.Time
}

// pictureOthers saves a thumbnail of up to maxInventoryShots windows of
// other apps.
func (w *Watcher) pictureOthers(l *Look, recs []teamsvideo.WindowRecord) {
	n := 0
	for _, r := range recs {
		if r.IsTeams() || r.Layer != 0 || r.Width < 100 || r.Height < 60 {
			continue
		}
		if n >= maxInventoryShots {
			return
		}
		n++
		fr, err := captureWindowByID(r.ID)
		if err != nil || fr == nil {
			continue
		}
		if jpegBytes, err := encodeJPEG(fr.pix, fr.width, fr.height, thumbWidth, 80); err == nil {
			w.cfg.OnWindowShot(l.ID, r.ID, true, jpegBytes)
		}
	}
}

// Signature grid: a window is reduced to sigW x sigH cells of mean
// brightness, enough to tell a changed layout from people moving.
const (
	sigW = 48
	sigH = 27
)

// lumaSig is fr's brightness on a sigW x sigH grid (each cell sampled at
// a few points).
func lumaSig(pix []byte, width, height int) []uint8 {
	sig := make([]uint8, sigW*sigH)
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return sig
	}
	for cy := 0; cy < sigH; cy++ {
		for cx := 0; cx < sigW; cx++ {
			var sum, n int
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					x := (cx*3 + sx) * width / (sigW * 3)
					y := (cy*3 + sy) * height / (sigH * 3)
					i := (y*width + x) * 3
					sum += (299*int(pix[i]) + 587*int(pix[i+1]) + 114*int(pix[i+2])) / 1000
					n++
				}
			}
			sig[cy*sigW+cx] = uint8(sum / n)
		}
	}
	return sig
}

// sigDiff is the mean absolute brightness difference per cell (0-255);
// 255 if the signatures can't be compared.
func sigDiff(a, b []uint8) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 255
	}
	var sum int
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(len(a))
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

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
