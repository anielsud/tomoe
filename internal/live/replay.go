package live

import (
	"fmt"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// replayWindowDuration is how much audio one VAD window holds, and so how
// far Replay's virtual clock advances per window.
const replayWindowDuration = time.Duration(vadWindowSize) * time.Second / vadSampleRate

// Replay runs recorded audio through the same per-window pipeline a live
// session uses (VAD, the optional pass-1 streaming decode, pass 2 or
// single-pass decode, speaker assignment) and returns the final segments
// in the order they were created. It exists to compare pipeline settings
// on real recordings: see `tomoe session replay`.
//
// Time is virtual: the clock advances one window's worth of audio per
// window fed, so a long meeting replays as fast as the models can decode
// it while segment timestamps and speaker.Tracker's time-based rules see
// the same timing they would have live.
//
// Unlike a live session, Replay is deterministic. Both sources are fed from
// one goroutine, interleaved window by window, and pass 2 runs as soon as
// each segment completes rather than on refineWorker. A live session's
// pass 2 lags by however long decoding takes, so Replay shows the
// no-backlog case: the refinement queue never fills.
//
// mic or monitor may be nil, not both. cfg's capturers are ignored. If
// cfg.Tracker is set, it's switched to the virtual clock and back to
// time.Now when Replay returns.
func Replay(cfg Config, mic, monitor []float32) ([]session.Segment, error) {
	r, err := ReplayDetailed(cfg, mic, monitor)
	if err != nil {
		return nil, err
	}
	return r.Segments, nil
}

// ReplayResult is ReplayDetailed's output.
type ReplayResult struct {
	// Segments are the final segments in creation order (see Replay).
	Segments []session.Segment
	// Pass1Text is each segment's pass-1 text as it stood when the
	// utterance ended, by segment ID. Only set with a StreamingEngine, and
	// only for segments pass 1 produced text for.
	Pass1Text map[string]string
	// Drafts are the pass-1 lines as first shown (two-pass only), in the
	// order they appeared, including those a turn later absorbed.
	Drafts []session.Segment
}

// ReplayDetailed is Replay that also reports pass 1's text per segment, for
// scoring the streaming pass separately from the final one (see `tomoe
// eval`).
func ReplayDetailed(cfg Config, mic, monitor []float32) (*ReplayResult, error) {
	if mic == nil && monitor == nil {
		return nil, fmt.Errorf("no audio to replay")
	}
	cfg.MicCapturer, cfg.MonitorCapturer = nil, nil
	c := New(cfg)

	virtual := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return virtual }
	c.now = clock
	c.startTime = virtual
	if cfg.Tracker != nil {
		cfg.Tracker.SetClock(clock)
		defer cfg.Tracker.SetClock(time.Now)
	}

	var sources []*sourceState
	var tracks [][]float32
	for _, t := range []struct {
		source  SourceType
		samples []float32
	}{{SourceMic, mic}, {SourceMonitor, monitor}} {
		if t.samples == nil {
			continue
		}
		st := c.newSourceState(t.source)
		if st == nil {
			return nil, fmt.Errorf("creating VAD for %s (model %q)", t.source, cfg.VADPath)
		}
		defer st.close()
		sources = append(sources, st)
		tracks = append(tracks, t.samples)
	}

	var order []string
	final := make(map[string]session.Segment)
	pass1 := make(map[string]string)
	var drafts []session.Segment
	record := func(seg session.Segment) {
		if seg.Status == session.StatusRemoved {
			delete(final, seg.ID)
			return
		}
		if _, seen := final[seg.ID]; !seen {
			order = append(order, seg.ID)
		}
		final[seg.ID] = seg
		if seg.Status == "pending" {
			if _, seen := pass1[seg.ID]; !seen {
				drafts = append(drafts, seg)
			}
			pass1[seg.ID] = seg.Text
		}
	}
	// collect empties the output channels before running any queued
	// refinement, so a segment's pass-1 "pending" version (sent before its
	// job was queued) is always recorded before the refined one replaces it.
	collect := func() {
		for {
			select {
			case seg := <-c.segmentCh:
				record(seg)
				continue
			case seg := <-c.segmentUpdateCh:
				record(seg)
				continue
			default:
			}
			select {
			case job := <-c.refineCh:
				if seg, ok := c.refine(job); ok {
					record(seg)
					for _, id := range job.absorb {
						record(session.Segment{ID: id, Status: session.StatusRemoved})
					}
				}
				continue
			default:
			}
			return
		}
	}

	longest := 0
	for _, t := range tracks {
		longest = max(longest, len(t)/vadWindowSize)
	}
	for w := 0; w < longest; w++ {
		for i, st := range sources {
			if end := (w + 1) * vadWindowSize; end <= len(tracks[i]) {
				c.processWindow(st, tracks[i][w*vadWindowSize:end])
			}
		}
		virtual = virtual.Add(replayWindowDuration)
		collect()
		if cfg.ReplayProgress != nil && (w%replayProgressEvery == 0 || w == longest-1) {
			cfg.ReplayProgress(w+1, longest)
		}
	}
	for _, st := range sources {
		c.finishSource(st)
		collect()
	}

	segs := make([]session.Segment, 0, len(order))
	for _, id := range order {
		if seg, ok := final[id]; ok {
			segs = append(segs, seg)
		}
	}
	if cfg.TurnMode {
		fmt.Printf("Turn mode: %v\n", c.TurnCuts())
	}
	return &ReplayResult{Segments: segs, Pass1Text: pass1, Drafts: drafts}, nil
}

// replayProgressEvery is how many windows (about 32 s of audio) pass
// between Config.ReplayProgress calls.
const replayProgressEvery = 1000
