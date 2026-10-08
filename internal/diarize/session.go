package diarize

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// minWords is MeetingConfig.MinSpeakerWords, applied to the final
	// labels.
	minWords int
	opts     SessionOptions

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
	// hints are the meeting window's name reads (session time); names the
	// timeline speakers they name (see nameSpeakers); renames the names
	// the user gave, which beat both.
	hints   []Hint
	names   map[int]string
	renames map[int]string
}

// SessionOptions are a SessionDiarizer's callbacks; any may be nil.
type SessionOptions struct {
	// OnChanged gets lines a recluster relabeled, after the lock is
	// released.
	OnChanged func([]session.Segment)
	// OnSpeakerChange is called when a voice starts that wasn't talking
	// just before (see StreamConfig.OnSpeakerChange).
	OnSpeakerChange func()
}

// NewSessionDiarizer starts diarizing a meeting in lang, or returns nil and
// why if it's off or can't run (the caller then diarizes after the
// meeting as before). Call SetSessionLocked once the session exists.
func NewSessionDiarizer(m config.MeetingConfig, status *models.Status, lang string, lock sync.Locker, opts SessionOptions) (*SessionDiarizer, error) {
	if !m.DiarizeDuringMeeting {
		return nil, nil
	}
	sc, err := StreamConfigFor(m, status, lang)
	if err != nil {
		return nil, err
	}
	d := &SessionDiarizer{lock: lock, split: m.SplitOnSpeakerChange, minWords: m.MinSpeakerWords, opts: opts, liveTo: map[string]string{}, renames: map[int]string{}}
	sc.OnTimeline = d.apply
	sc.OnSpeakerChange = opts.OnSpeakerChange
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
	if d.opts.OnChanged != nil && len(changed) > 0 {
		d.opts.OnChanged(changed)
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
	through := tl.Through + d.offset
	assigned, labels := session.DiarizationLabels(segs, tl.Turns, d.labelsLocked(through))
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
// label has mapped to so far, else NewSpeakerLabel, so new lines never
// show the live pass's own numbering once timeline labels are showing.
func (d *SessionDiarizer) LabelNewLocked(seg *session.Segment) {
	if d.timeline == nil {
		return
	}
	probe := []session.Segment{*seg}
	if assigned, labels := session.DiarizationLabels(probe, d.timeline.Turns, d.labelsLocked(d.timeline.Through+d.offset)); assigned[0] >= 0 && seg.EndTime <= d.timeline.Through+d.offset {
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), labels[assigned[0]]
		return
	}
	if !session.Diarizable(*seg) {
		return
	}
	live := seg.LiveLabel()
	label, ok := d.liveTo[live]
	if (!ok || label == "") && d.timeline != nil {
		// The live pass doesn't know this voice, but the meeting window
		// says who's talking: if a timeline speaker already carries that
		// name, it's them. Without this, a 1:1 showed the other person
		// under a new provisional label every time the live pass split
		// off a new cluster for their voice (19 in a 20-minute call).
		if name := d.hintDuringLocked(seg.StartTime, seg.EndTime); name != "" {
			if k, found := d.speakerNamedLocked(name); found {
				label, ok = d.labelFor(k), true
			}
		}
	}
	if !ok || label == "" {
		// A voice the timeline hasn't placed yet. The live pass numbers
		// speakers its own way, so its "Person 5" may be a different
		// person from the timeline's: show a neutral label until the next
		// recluster places it.
		label = NewSpeakerLabel
		if name := session.HintName(live); name != "" {
			label += " (" + name + ")"
		}
	}
	// The meeting window may already say who it is: show that,
	// provisionally, until the timeline confirms or corrects it.
	if name := d.hintDuringLocked(seg.StartTime, seg.EndTime); name != "" && session.HintName(label) == "" && !d.isRenamedLabel(label) {
		if label == NewSpeakerLabel {
			label = name + "?"
		} else {
			label += " (" + name + "?)"
		}
	}
	seg.LiveSpeaker, seg.Speaker = live, label
}

// labelsLocked labels the timeline's speakers: the user's rename, else
// "Person N (Name)" for a speaker hints have named by the time through,
// else "Person N".
func (d *SessionDiarizer) labelsLocked(through float64) map[int]string {
	d.names = nameSpeakers(d.timeline.Turns, d.hints, through)
	out := make(map[int]string, len(d.timeline.Labels))
	for k := range d.timeline.Labels {
		out[k] = d.labelFor(k)
	}
	return out
}

func (d *SessionDiarizer) labelFor(k int) string {
	if name := d.renames[k]; name != "" {
		return name
	}
	if name := d.names[k]; name != "" {
		return fmt.Sprintf("Person %d (%s)", k+1, name)
	}
	return fmt.Sprintf("Person %d", k+1)
}

// speakerNamedLocked finds the timeline speaker named name (by the meeting
// window or the user), if exactly one is.
func (d *SessionDiarizer) speakerNamedLocked(name string) (int, bool) {
	found, n := -1, 0
	for k := range d.timeline.Labels {
		if SameName(d.renames[k], name) || SameName(d.names[k], name) {
			found = k
			n++
		}
	}
	return found, n == 1
}

func (d *SessionDiarizer) isRenamedLabel(label string) bool {
	for _, name := range d.renames {
		if name == label {
			return true
		}
	}
	return false
}

// hintDuringLocked is the last name read while a line from start to end
// was spoken, or "".
func (d *SessionDiarizer) hintDuringLocked(start, end float64) string {
	for i := len(d.hints) - 1; i >= 0; i-- {
		h := d.hints[i]
		if t := h.T - hintLag; h.Name != "" && t >= start && t <= end {
			return h.Name
		}
	}
	return ""
}

// AddHint records that the meeting window showed name under the ring at
// session time t. Takes the lock.
func (d *SessionDiarizer) AddHint(t float64, name string) {
	d.lock.Lock()
	d.hints = append(d.hints, Hint{T: t, Name: name})
	d.lock.Unlock()
}

// AddCandidates records that several tiles were lit at session time t
// with these names (all read): one of them is speaking. Takes the lock.
func (d *SessionDiarizer) AddCandidates(t float64, names []string) {
	d.lock.Lock()
	d.hints = append(d.hints, Hint{T: t, Candidates: append([]string(nil), names...)})
	d.lock.Unlock()
}

// NeedsNames reports whether anyone who spoke in the last minute has no
// name yet (or there's no timeline yet), so the meeting window should be
// watched closely. Takes the lock.
func (d *SessionDiarizer) NeedsNames() bool {
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.timeline == nil {
		return true
	}
	through := d.timeline.Through + d.offset
	for _, t := range d.timeline.Turns {
		if t.End >= through-60 && d.renames[t.Speaker] == "" && d.names[t.Speaker] == "" {
			return true
		}
	}
	return false
}

// RenameLocked gives the timeline speaker currently labeled label the
// name name, which then beats any name the meeting window suggests, and
// relabels their lines, returning the ones that changed. False if no
// timeline speaker has that label.
func (d *SessionDiarizer) RenameLocked(label, name string) ([]session.Segment, bool) {
	if d.timeline == nil || name == "" {
		return nil, false
	}
	for k := range d.timeline.Labels {
		if d.labelFor(k) == label {
			d.renames[k] = name
			return d.relabelLocked(), true
		}
	}
	return nil, false
}

// NewSpeakerLabel labels a line whose voice the timeline hasn't placed yet.
const NewSpeakerLabel = "New speaker"

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
	tl.Through = 1e18 // everything
	d.timeline = &tl
	labels := d.labelsLocked(tl.Through)
	if d.split {
		d.sess.Segments, _ = session.SplitByDiarization(d.sess.Segments, tl.Turns, labels)
	} else {
		d.relabelLocked()
	}
	d.settleProvisionalLocked(tl.Turns, labels)
	session.AbsorbSmallSpeakers(d.sess.Segments, d.minWords, d.isRenamedLabel)
	id := d.sess.ID
	d.lock.Unlock()
	st := d.stream.Stats()
	fmt.Printf("session %s: final speaker labels %.1fs after the meeting ended (diarizer was %.1fs behind then, at most %.1fs; %d reclusters took %.1fs, slowest %.2fs; %d windows, %d fingerprints; %d caught up in parallel in %.1fs)\n",
		id, time.Since(began).Seconds(), st.BehindAtEnd, st.MaxBehind, st.Reclusters, st.ReclusterTotal, st.ReclusterMax, st.Windows, st.Fingerprints, st.CatchUpWindows, st.CatchUpSeconds)

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
	// What replaying the fingerprints needs besides them: where the
	// stream's audio starts in session time, and the settings used.
	info, _ := json.MarshalIndent(StreamInfo{
		Offset: d.offset, Stride: d.stream.cfg.Stride, ReclusterSeconds: d.stream.cfg.ReclusterSeconds, Params: d.stream.cfg.Params, MinSpeakerSeconds: d.stream.cfg.MinSpeakerSeconds, Stats: &st,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "diarization.json"), info, 0o644)
	return nil
}

// StreamInfo is saved next to a session's fingerprints
// (diarization.json): the session time the diarized audio starts at, and
// the settings it was diarized with.
type StreamInfo struct {
	Offset            float64 `json:"offset_seconds"`
	Stride            int     `json:"stride"`
	ReclusterSeconds  float64 `json:"recluster_seconds"`
	Params            Params  `json:"params"`
	MinSpeakerSeconds float64 `json:"min_speaker_seconds,omitempty"`
	// Stats is how the diarizer kept up during the meeting.
	Stats *StreamStats `json:"stats,omitempty"`
}

// Abort stops without using the result (the session never started).
func (d *SessionDiarizer) Abort() {
	_, _ = d.stream.Finish()
	d.stream.Close()
}

// settleProvisionalLocked gives every line still carrying a provisional
// label ("New speaker", "Ana?") once the meeting is over a final one: the
// speaker of the nearest turn within 10 s, else the live label. These are
// lines no turn overlaps (very short, or in a gap the timeline leaves).
func (d *SessionDiarizer) settleProvisionalLocked(turns []session.DiarizeSegment, labels map[int]string) {
	for i := range d.sess.Segments {
		seg := &d.sess.Segments[i]
		if !isProvisional(seg.Speaker) {
			continue
		}
		mid := (seg.StartTime + seg.EndTime) / 2
		best, bestD := -1, 10.0
		for _, t := range turns {
			dist := 0.0
			switch {
			case mid < t.Start:
				dist = t.Start - mid
			case mid > t.End:
				dist = mid - t.End
			}
			if dist < bestD {
				best, bestD = t.Speaker, dist
			}
		}
		if best >= 0 {
			seg.Speaker = labels[best]
		} else {
			seg.Speaker = seg.LiveLabel()
		}
	}
}

// isProvisional reports whether label is one LabelNewLocked gives while
// the timeline hasn't placed a voice yet.
func isProvisional(label string) bool {
	return strings.HasPrefix(label, NewSpeakerLabel) || strings.HasSuffix(label, "?") || strings.HasSuffix(label, "?)")
}
