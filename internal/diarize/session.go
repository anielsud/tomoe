package diarize

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// SessionDiarizer diarizes a recording session while it's recorded
// (config.MeetingConfig.DiarizeDuringMeeting): it feeds the monitor audio
// to a Stream and relabels the session's lines from each recluster's
// timeline, so labels improve as the meeting goes on and the final ones
// are ready when it ends, with no post-meeting pass. See
// docs/speaker-pipeline-design.md. Used by the GUI backend and the CLI
// daemon alike.
//
// The live labels (speaker.Tracker) still label each new line at once;
// once a timeline covers a line, the timeline's speaker replaces it.
// Segment.LiveSpeaker keeps the live label, whose video-hint names the
// timeline's speakers are named by.
//
// The session is shared with whoever collects its segments, under lock;
// methods named ...Locked expect the caller to hold it.
type SessionDiarizer struct {
	stream *Stream
	lock   sync.Locker
	split  bool
	// onChanged, if set, gets lines a recluster relabeled, after lock is
	// released.
	onChanged func([]session.Segment)

	// Set on the pipeline goroutine's first window: the session time the
	// stream's audio starts at.
	offset  float64
	started bool

	// Guarded by lock.
	sess     *session.Session
	timeline *Timeline
	// liveTo maps a live label to the timeline label its lines got, for
	// labeling new lines the timeline doesn't cover yet.
	liveTo map[string]string
}

// NewSessionDiarizer starts diarizing a meeting in lang, or returns nil and
// why if it's off or can't run (the caller then diarizes after the
// meeting as before). Call SetSessionLocked once the session exists.
func NewSessionDiarizer(m config.MeetingConfig, status *models.Status, lang string, lock sync.Locker, onChanged func([]session.Segment)) (*SessionDiarizer, error) {
	if !m.DiarizeDuringMeeting {
		return nil, nil
	}
	sc, err := StreamConfigFor(m, status, lang)
	if err != nil {
		return nil, err
	}
	d := &SessionDiarizer{lock: lock, split: m.SplitOnSpeakerChange, onChanged: onChanged, liveTo: map[string]string{}}
	sc.OnTimeline = d.apply
	if d.stream, err = NewStream(sc); err != nil {
		return nil, err
	}
	return d, nil
}

// Describe summarizes the settings, for logs.
func (d *SessionDiarizer) Describe() string {
	return fmt.Sprintf("every %d windows, recluster every %.0fs", d.stream.cfg.Stride, d.stream.cfg.ReclusterSeconds)
}

// SetSessionLocked attaches the session the timeline relabels. Until then
// timelines are ignored.
func (d *SessionDiarizer) SetSessionLocked(sess *session.Session) { d.sess = sess }

// Feed is live.Config.MonitorAudio.
func (d *SessionDiarizer) Feed(samples []float32, endTime float64) {
	if !d.started {
		d.offset, d.started = endTime-float64(len(samples))/16000, true
	}
	d.stream.Feed(samples)
}

// apply relabels the session's lines from a recluster's timeline. Runs on
// the Stream's goroutine.
func (d *SessionDiarizer) apply(tl Timeline) {
	if tl.Final {
		return // Finish applies the final timeline
	}
	tl.Turns = d.shift(tl)
	d.lock.Lock()
	if d.sess == nil {
		d.lock.Unlock()
		return
	}
	d.timeline = &tl
	changed := d.relabelLocked()
	d.lock.Unlock()
	if d.onChanged != nil && len(changed) > 0 {
		d.onChanged(changed)
	}
}

// shift moves tl's turns from stream time to session time.
func (d *SessionDiarizer) shift(tl Timeline) []session.DiarizeSegment {
	out := make([]session.DiarizeSegment, len(tl.Turns))
	for i, t := range tl.Turns {
		t.Start += d.offset
		t.End += d.offset
		out[i] = t
	}
	return out
}

// relabelLocked gives each line the timeline covers its speaker, returning
// the lines whose label changed.
func (d *SessionDiarizer) relabelLocked() []session.Segment {
	tl := d.timeline
	segs := d.sess.Segments
	assigned, labels := session.DiarizationLabels(segs, tl.Turns, tl.Labels)
	through := tl.Through + d.offset
	liveTime := map[string]map[string]float64{}
	var changed []session.Segment
	for i := range segs {
		seg := &segs[i]
		if assigned[i] < 0 || seg.EndTime > through {
			continue
		}
		label := labels[assigned[i]]
		live := seg.LiveLabel()
		if liveTime[live] == nil {
			liveTime[live] = map[string]float64{}
		}
		liveTime[live][label] += seg.EndTime - seg.StartTime
		if seg.Speaker != label {
			seg.LiveSpeaker = live
			seg.Speaker = label
			changed = append(changed, *seg)
		}
	}
	d.liveTo = map[string]string{}
	for live, byLabel := range liveTime {
		best, bestT := "", 0.0
		for label, t := range byLabel {
			if t > bestT || (t == bestT && label < best) {
				best, bestT = label, t
			}
		}
		d.liveTo[live] = best
	}
	return changed
}

// LabelNewLocked relabels a line arriving from the live pass (or a
// revision of one) before it's stored: the timeline's speaker if the
// timeline already covers it, else whichever timeline speaker its live
// label has mapped to so far, so new lines don't show the live pass's own
// numbering.
func (d *SessionDiarizer) LabelNewLocked(seg *session.Segment) {
	if d.timeline == nil {
		return
	}
	probe := []session.Segment{*seg}
	if assigned, labels := session.DiarizationLabels(probe, d.timeline.Turns, d.timeline.Labels); assigned[0] >= 0 && seg.EndTime <= d.timeline.Through+d.offset {
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), labels[assigned[0]]
		return
	}
	if label, ok := d.liveTo[seg.LiveLabel()]; ok && label != "" && label != seg.Speaker {
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), label
	}
}

// Finish waits for the stream to catch up, applies the final timeline to
// the session (splitting lines at speaker changes if that's on), and saves
// the fingerprints as diarization.gob in dir. Call once every segment is
// in. On error the session's labels are left as they were, for the caller
// to fall back to the post-meeting pass.
func (d *SessionDiarizer) Finish(dir string) error {
	defer d.stream.Close()
	began := time.Now()
	tl, err := d.stream.Finish()
	if err != nil {
		return err
	}
	tl.Turns = d.shift(tl)
	d.lock.Lock()
	if d.split {
		d.sess.Segments, _ = session.SplitByDiarization(d.sess.Segments, tl.Turns, tl.Labels)
	} else {
		tl.Through = 1e18 // everything
		d.timeline = &tl
		d.relabelLocked()
	}
	id := d.sess.ID
	d.lock.Unlock()
	fmt.Printf("session %s: final speaker labels %.1fs after the meeting ended\n", id, time.Since(began).Seconds())

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil // the labels are applied; the fingerprints are a bonus
	}
	f, err := os.Create(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(d.stream.Prepared()); err != nil {
		fmt.Printf("session %s: couldn't save fingerprints: %v\n", id, err)
	}
	return nil
}

// Abort stops without using the result (the session never started).
func (d *SessionDiarizer) Abort() {
	_, _ = d.stream.Finish()
	d.stream.Close()
}
