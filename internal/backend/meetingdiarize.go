package backend

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// meetingDiarizer diarizes a recording session while it's recorded
// (config.MeetingConfig.DiarizeDuringMeeting): it feeds the monitor audio
// to a diarize.Stream and relabels the session's lines from each
// recluster's timeline, so labels improve as the meeting goes on and the
// final ones are ready when it ends, with no post-meeting pass. See
// docs/speaker-pipeline-design.md.
//
// The live labels (speaker.Tracker) still label each new line at once;
// once a timeline covers a line, the timeline's speaker replaces it.
// Segment.LiveSpeaker keeps the live label, whose video-hint names the
// timeline's speakers are named by.
type meetingDiarizer struct {
	app    *App
	sess   *session.Session
	stream *diarize.Stream
	split  bool // split lines at speaker changes when final

	// Set on the pipeline goroutine's first window: the session time the
	// stream's audio starts at.
	offset  float64
	started bool

	// Guarded by app.mu, like sess.
	timeline *diarize.Timeline
	// liveTo maps a live label to the timeline label its lines got, for
	// labeling new lines the timeline doesn't cover yet.
	liveTo map[string]string
}

// newMeetingDiarizer starts diarizing sess during the meeting, or returns
// nil (with the reason logged) if it's off or can't run, in which case the
// post-meeting pass runs as before.
func newMeetingDiarizer(a *App, cfg *config.Config, status *models.Status, sess *session.Session, lang string) *meetingDiarizer {
	if !cfg.Meeting.DiarizeDuringMeeting {
		return nil
	}
	sc, err := diarize.StreamConfigFor(cfg.Meeting, status, lang)
	if err != nil {
		fmt.Printf("diarize during meeting: %v; diarizing after the meeting instead\n", err)
		return nil
	}
	md := &meetingDiarizer{app: a, sess: sess, split: cfg.Meeting.SplitOnSpeakerChange, liveTo: map[string]string{}}
	sc.OnTimeline = md.apply
	if md.stream, err = diarize.NewStream(sc); err != nil {
		fmt.Printf("diarize during meeting: %v; diarizing after the meeting instead\n", err)
		return nil
	}
	fmt.Printf("session %s: diarizing during the meeting (every %d windows, recluster every %.0fs)\n", sess.ID, sc.Stride, sc.ReclusterSeconds)
	return md
}

// feed is live.Config.MonitorAudio.
func (md *meetingDiarizer) feed(samples []float32, endTime float64) {
	if !md.started {
		md.offset, md.started = endTime-float64(len(samples))/16000, true
	}
	md.stream.Feed(samples)
}

// apply relabels the session's lines from a recluster's timeline and sends
// the changed ones to the frontend. Runs on the Stream's goroutine.
func (md *meetingDiarizer) apply(tl diarize.Timeline) {
	if tl.Final {
		return // finish handles the final timeline
	}
	turns := md.shift(tl)
	a := md.app
	a.mu.Lock()
	if md.sess == nil { // StartSession hasn't finished
		a.mu.Unlock()
		return
	}
	md.timeline = &tl
	md.timeline.Turns = turns
	changed := md.relabelLocked()
	visible := a.currentSess == md.sess
	a.mu.Unlock()
	if visible {
		for _, seg := range changed {
			wailsRuntime.EventsEmit(a.ctx, "transcript:segment:update", seg)
		}
	}
}

// shift moves tl's turns from stream time to session time.
func (md *meetingDiarizer) shift(tl diarize.Timeline) []session.DiarizeSegment {
	out := make([]session.DiarizeSegment, len(tl.Turns))
	for i, t := range tl.Turns {
		t.Start += md.offset
		t.End += md.offset
		out[i] = t
	}
	return out
}

// relabelLocked gives each line the timeline covers its speaker, returning
// the lines whose label changed. Caller holds app.mu.
func (md *meetingDiarizer) relabelLocked() []session.Segment {
	tl := md.timeline
	segs := md.sess.Segments
	assigned, labels := session.DiarizationLabels(segs, tl.Turns, tl.Labels)
	through := tl.Through + md.offset
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
	md.liveTo = map[string]string{}
	for live, byLabel := range liveTime {
		best, bestT := "", 0.0
		for label, t := range byLabel {
			if t > bestT || (t == bestT && label < best) {
				best, bestT = label, t
			}
		}
		md.liveTo[live] = best
	}
	return changed
}

// labelNew relabels a line arriving from the live pass (or a revision of
// one) before it's stored: the timeline's speaker if the timeline already
// covers it, else whichever timeline speaker its live label has mapped to
// so far, so new lines don't show the live pass's own numbering. Caller
// holds app.mu.
func (md *meetingDiarizer) labelNewLocked(seg *session.Segment) {
	if md.timeline == nil {
		return
	}
	probe := []session.Segment{*seg}
	if assigned, labels := session.DiarizationLabels(probe, md.timeline.Turns, md.timeline.Labels); assigned[0] >= 0 && seg.EndTime <= md.timeline.Through+md.offset {
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), labels[assigned[0]]
		return
	}
	if label, ok := md.liveTo[seg.LiveLabel()]; ok && label != "" && label != seg.Speaker {
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), label
	}
}

// finish waits for the stream to catch up, applies the final timeline
// (splitting lines at speaker changes if that's on), and saves the
// fingerprints with the session. Returns false if diarization failed, so
// the caller can fall back to the post-meeting pass.
func (md *meetingDiarizer) finish() bool {
	defer md.stream.Close()
	began := time.Now()
	tl, err := md.stream.Finish()
	if err != nil {
		fmt.Printf("session %s: diarizing during the meeting failed: %v\n", md.sess.ID, err)
		return false
	}
	turns := md.shift(tl)
	a := md.app
	a.mu.Lock()
	if md.split {
		md.sess.Segments, _ = session.SplitByDiarization(md.sess.Segments, turns, tl.Labels)
	} else {
		md.timeline = &tl
		md.timeline.Turns = turns
		md.timeline.Through = 1e18 // everything
		md.relabelLocked()
	}
	a.mu.Unlock()
	fmt.Printf("session %s: final speaker labels %.1fs after the meeting ended\n", md.sess.ID, time.Since(began).Seconds())

	path := filepath.Join(config.SessionDir(), md.sess.ID, "diarization.gob")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.Create(path); err == nil {
			if err := gob.NewEncoder(f).Encode(md.stream.Prepared()); err != nil {
				fmt.Printf("session %s: couldn't save fingerprints: %v\n", md.sess.ID, err)
			}
			f.Close()
		}
	}
	return true
}

// abort stops the stream without using its result (the session never
// started).
func (md *meetingDiarizer) abort() {
	_, _ = md.stream.Finish()
	md.stream.Close()
}
