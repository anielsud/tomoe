package backend

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/live"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// LookView is the frontend's view of a videohint.Look: one capture of the
// meeting window and what was found in it, for the hint timeline.
type LookView struct {
	ID int `json:"id"`
	// Time is wall-clock (RFC3339); SessionTime seconds into the session,
	// the transcript's time base.
	Time        string                `json:"time"`
	SessionTime float64               `json:"sessionTime"`
	Stage       string                `json:"stage"`
	Detail      string                `json:"detail"`
	Name        string                `json:"name,omitempty"`
	FromCache   bool                  `json:"fromCache,omitempty"`
	Usable      bool                  `json:"usable"`
	Ring        *videohint.RingMatch  `json:"ring,omitempty"`
	Rings       []videohint.RingMatch `json:"rings,omitempty"`
	Candidates  []string              `json:"candidates,omitempty"`
	Width       int                   `json:"width,omitempty"`
	Height      int                   `json:"height,omitempty"`
	// Thumb is a data URI, sent with live looks that have their own
	// thumbnail; ThumbOf names the look whose thumbnail this one shares.
	// Saved sessions' thumbnails come from GetLookThumb.
	Thumb   string `json:"thumb,omitempty"`
	ThumbOf int    `json:"thumbOf,omitempty"`
}

func toLookView(l videohint.Look, start time.Time, withThumb bool) LookView {
	v := LookView{
		ID: l.ID, Time: l.Time.Format(time.RFC3339Nano), SessionTime: l.Time.Sub(start).Seconds(),
		Stage: string(l.Stage), Detail: l.Detail, Name: l.Name, FromCache: l.FromCache, Usable: l.Usable,
		Ring: l.Ring, Rings: l.Rings, Candidates: l.Candidates, Width: l.Width, Height: l.Height, ThumbOf: l.ThumbOf,
	}
	if withThumb && len(l.Thumb) > 0 {
		v.Thumb = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(l.Thumb)
	}
	return v
}

// hintSession is what a recording session's hint watcher feeds.
type hintSession struct {
	sess        *session.Session
	coordinator *live.Coordinator
	diar        *diarize.SessionDiarizer // nil: the previous pipeline (names go to the live tracker)
	log         *videohint.LookLog
	watcher     *videohint.Watcher
}

// newHintWatcher creates the session's meeting-window watcher. It runs
// once startHintWatcher has the session; Burst may be called before.
func (a *App) newHintWatcher() (*videohint.Watcher, *hintSession) {
	hs := &hintSession{}
	learn, check := a.cfg.Meeting.VideoHintTiming()
	hs.watcher = videohint.NewWatcher(videohint.WatchConfig{
		LearnInterval: learn,
		CheckInterval: check,
		NeedsLearning: func() bool {
			if a.cfg.Meeting.RecordForTuning {
				return true // look at the learning rate throughout
			}
			if hs.diar != nil {
				return hs.diar.NeedsNames()
			}
			return a.tracker == nil || a.tracker.HasUnnamedSpeakers()
		},
		OnLook: func(l videohint.Look) { a.onLook(hs, l) },
	})
	if a.cfg.Meeting.RecordForTuning {
		hs.watcher.SetOnFullFrame(func(id int, jpeg []byte) {
			if err := hs.log.WriteFull(id, jpeg); err != nil {
				fmt.Printf("videohint: saving frame %d: %v\n", id, err)
			}
		})
	}
	return hs.watcher, hs
}

// startHintWatcher runs hs's watcher for the session until ctx ends, and
// turns the live pass's "this speaker has no name yet" signals into
// bursts of looks.
func (a *App) startHintWatcher(ctx context.Context, hs *hintSession) {
	hs.log = videohint.NewLookLog(filepath.Join(config.SessionDir(), hs.sess.ID))
	a.videoHintMu.Lock()
	a.videoLooks = nil
	a.hints = hs
	a.videoHintMu.Unlock()
	go hs.watcher.Run(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hs.coordinator.HintNeeded():
				hs.watcher.Burst()
			}
		}
	}()
}

// onLook records a look with the session, hands a read name to whatever
// attributes names, and sends the look to the hint timeline.
func (a *App) onLook(hs *hintSession, l videohint.Look) {
	if err := hs.log.Write(l); err != nil {
		fmt.Printf("videohint: recording look %d: %v\n", l.ID, err)
	}
	if l.Usable && l.Name != "" {
		if hs.diar != nil {
			hs.diar.AddHint(hs.coordinator.SessionTime(l.Time), l.Name)
		} else if a.tracker != nil {
			a.tracker.SetHintForRecent(l.Name, videohint.HintAttachMaxAge)
		}
	} else if hs.diar != nil && len(l.Candidates) > 1 {
		hs.diar.AddCandidates(hs.coordinator.SessionTime(l.Time), l.Candidates)
	}
	meta := l
	meta.Thumb = nil
	a.videoHintMu.Lock()
	current := a.hints == hs
	if current {
		a.videoLooks = append(a.videoLooks, meta)
	}
	a.videoHintMu.Unlock()
	if current {
		wailsRuntime.EventsEmit(a.ctx, "videohint:look", toLookView(l, hs.sess.CreatedAt, true))
	}
}

// GetVideoHintLooks returns a session's looks at the meeting window,
// oldest first, without thumbnails (see GetLookThumb): the current
// recording's if id is "" or its ID, else a saved session's.
func (a *App) GetVideoHintLooks(id string) ([]LookView, error) {
	a.fixSignals()
	a.videoHintMu.Lock()
	if hs := a.hints; hs != nil && (id == "" || id == hs.sess.ID) {
		out := make([]LookView, len(a.videoLooks))
		for i, l := range a.videoLooks {
			out[i] = toLookView(l, hs.sess.CreatedAt, false)
		}
		a.videoHintMu.Unlock()
		return out, nil
	}
	a.videoHintMu.Unlock()
	if id == "" {
		return nil, nil
	}
	sess, err := a.store.Load(id)
	if err != nil {
		return nil, err
	}
	looks, err := videohint.ReadLooks(filepath.Join(config.SessionDir(), id))
	if err != nil {
		return nil, err
	}
	out := make([]LookView, len(looks))
	for i, l := range looks {
		out[i] = toLookView(l, sess.CreatedAt, false)
	}
	return out, nil
}

// GetLookThumb returns a look's thumbnail as a data URI ("" for the
// current recording means the current session).
func (a *App) GetLookThumb(id string, look int) (string, error) {
	a.fixSignals()
	if id == "" {
		a.videoHintMu.Lock()
		if a.hints != nil {
			id = a.hints.sess.ID
		}
		a.videoHintMu.Unlock()
	}
	data, err := videohint.ReadThumb(filepath.Join(config.SessionDir(), id), look)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// SaveLookForAnalysis copies a look to the hint-analysis folder (see
// config.HintAnalysisDir): its full-resolution frame while the current
// recording still holds it, else its thumbnail, plus its details.
// Returns where it went.
func (a *App) SaveLookForAnalysis(id string, look int) (string, error) {
	a.fixSignals()
	a.videoHintMu.Lock()
	hs := a.hints
	var found *videohint.Look
	if hs != nil && (id == "" || id == hs.sess.ID) {
		id = hs.sess.ID
		for i := range a.videoLooks {
			if a.videoLooks[i].ID == look {
				l := a.videoLooks[i]
				found = &l
			}
		}
	} else {
		hs = nil
	}
	a.videoHintMu.Unlock()
	dir := filepath.Join(config.SessionDir(), id)
	if found == nil {
		looks, err := videohint.ReadLooks(dir)
		if err != nil {
			return "", err
		}
		for i := range looks {
			if looks[i].ID == look {
				found = &looks[i]
			}
		}
	}
	if found == nil {
		return "", fmt.Errorf("look %d not found", look)
	}
	thumbID := found.ID
	if found.ThumbOf != 0 {
		thumbID = found.ThumbOf
	}
	var frame []byte
	if hs != nil {
		frame, _ = hs.watcher.FullFrame(thumbID)
	}
	if frame == nil {
		frame, _ = videohint.ReadThumb(dir, thumbID)
	}
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return videohint.SaveForAnalysis(config.HintAnalysisDir(), fmt.Sprintf("%s-%s-look%d", found.Time.Format("20060102-150405"), short, found.ID), *found, frame)
}

// RenameSpeaker names a speaker in a session's transcript ("" for the
// current recording). While recording with diarization during the
// meeting, the name sticks to that voice, beating any name the meeting
// window suggests; otherwise every line labeled label is renamed.
func (a *App) RenameSpeaker(id, label, name string) error {
	a.fixSignals()
	if name == "" || label == "" {
		return fmt.Errorf("a speaker and a name are needed")
	}
	if label == "You" {
		return fmt.Errorf("the microphone is always \"You\"")
	}
	a.mu.Lock()
	sess := a.currentSess
	if sess != nil && (id == "" || id == sess.ID) {
		var changed []session.Segment
		if md := a.meetingDiar; md != nil {
			if c, ok := md.RenameLocked(label, name); ok {
				changed = c
			}
		}
		if changed == nil {
			changed = renameLines(sess, label, name)
		}
		a.mu.Unlock()
		for _, seg := range changed {
			wailsRuntime.EventsEmit(a.ctx, "transcript:segment:update", seg)
		}
		return nil
	}
	a.mu.Unlock()
	saved, err := a.store.Load(id)
	if err != nil {
		return err
	}
	if len(renameLines(saved, label, name)) == 0 {
		return fmt.Errorf("no lines labeled %q", label)
	}
	return a.store.Save(saved)
}

// renameLines relabels sess's lines labeled label, keeping their live
// label, and returns them.
func renameLines(sess *session.Session, label, name string) []session.Segment {
	var changed []session.Segment
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if seg.Speaker == label {
			seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), name
			changed = append(changed, *seg)
		}
	}
	return changed
}
