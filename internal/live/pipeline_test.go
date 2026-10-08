package live

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// mockStreamingSession implements transcribe.StreamingSession; handleSegment
// only ever resets it.
type mockStreamingSession struct{ resets int }

func (m *mockStreamingSession) Feed([]float32) (string, error) { return "", nil }
func (m *mockStreamingSession) Reset()                         { m.resets++ }
func (m *mockStreamingSession) Close()                         {}

// runRefineWorker drains every job already queued on c.refineCh.
func runRefineWorker(c *Coordinator) {
	c.refineWG.Add(1)
	go c.refineWorker()
	close(c.refineCh)
	c.refineWG.Wait()
}

func receiveNone(t *testing.T, ch <-chan session.Segment, what string) {
	t.Helper()
	select {
	case seg := <-ch:
		t.Errorf("unexpected %s: %+v", what, seg)
	default:
	}
}

// fillRefineQueue leaves c.refineCh with no free slot.
func fillRefineQueue(c *Coordinator) {
	for len(c.refineCh) < cap(c.refineCh) {
		c.refineCh <- refinementJob{id: "filler"}
	}
}

func TestHandleSegment_NoPass1TextStillReachesPass2(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: " yes ", Language: "en"}}
	c := New(Config{Engine: eng})
	sess := &mockStreamingSession{}
	var live liveState

	c.handleSegment(SourceMic, make([]float32, 8000), sess, &live)

	receiveNone(t, c.segmentCh, "segment before pass 2 ran")
	if sess.resets != 1 {
		t.Errorf("streaming session resets = %d, want 1", sess.resets)
	}

	runRefineWorker(c)
	seg := <-c.segmentCh
	if seg.Text != "yes" || seg.Status != "" || seg.Speaker != "You" {
		t.Errorf("got %+v, want final \"yes\" from You", seg)
	}
	receiveNone(t, c.segmentUpdateCh, "update for a segment that was never announced")
	if got := c.Stats().MicSegments; got != 1 {
		t.Errorf("MicSegments = %d, want 1", got)
	}
}

func TestHandleSegment_NoPass1TextAndNoSpeechEmitsNothing(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "  "}}
	c := New(Config{Engine: eng})
	var live liveState

	c.handleSegment(SourceMonitor, make([]float32, 8000), &mockStreamingSession{}, &live)
	runRefineWorker(c)

	receiveNone(t, c.segmentCh, "segment for audio pass 2 found no speech in")
	receiveNone(t, c.segmentUpdateCh, "update for audio pass 2 found no speech in")
	if got := c.Stats().MonitorSegments; got != 0 {
		t.Errorf("MonitorSegments = %d, want 0", got)
	}
}

func TestHandleSegment_NoPass1TextQueueFullDecodesSynchronously(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "ok", Language: "en"}}
	c := New(Config{Engine: eng, SegmentBufferSize: 2})
	fillRefineQueue(c)
	var live liveState

	c.handleSegment(SourceMic, make([]float32, 8000), &mockStreamingSession{}, &live)

	seg := <-c.segmentCh
	if seg.Text != "ok" || seg.Status != "" {
		t.Errorf("got %+v, want final \"ok\" decoded without the queue", seg)
	}
	if got := c.Stats().MicSegments; got != 1 {
		t.Errorf("MicSegments = %d, want 1", got)
	}
}

func TestHandleSegment_QueueFullFinalizesPass1Text(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}, SegmentBufferSize: 2})
	fillRefineQueue(c)
	live := liveState{partial: "rough text", shown: "rough", id: "seg-7", speaker: "You", startTime: 1}

	c.handleSegment(SourceMic, make([]float32, 8000), &mockStreamingSession{}, &live)

	pending := <-c.segmentUpdateCh
	final := <-c.segmentUpdateCh
	if pending.Status != "pending" {
		t.Errorf("first update status = %q, want pending", pending.Status)
	}
	if final.ID != "seg-7" || final.Status != "" || final.Text != "rough text" {
		t.Errorf("second update = %+v, want seg-7 finalized with pass-1 text", final)
	}
	if live.id != "" {
		t.Error("live state not reset")
	}
}

func TestHandleSegment_BlankPartialKeepsShownText(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}})
	live := liveState{partial: "", shown: "hello there", id: "seg-3", speaker: "You", startTime: 1}

	c.handleSegment(SourceMic, make([]float32, 8000), &mockStreamingSession{}, &live)

	update := <-c.segmentUpdateCh
	if update.ID != "seg-3" || update.Text != "hello there" || update.Status != "pending" {
		t.Errorf("update = %+v, want seg-3 pending with the shown text", update)
	}
	job := <-c.refineCh
	if job.id != "seg-3" || job.pass1Text != "hello there" || job.unannounced {
		t.Errorf("job = %+v, want an announced refinement of seg-3", job)
	}
}

func TestFinishLive_RefinesOpenLiveSegment(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "refined"}}})
	live := liveState{partial: "rough", shown: "rough", id: "seg-9", speaker: "Person 1", startTime: 2, audio: make([]float32, 8000)}

	c.finishLive(SourceMonitor, &live)
	if live.id != "" {
		t.Error("live state not reset")
	}

	runRefineWorker(c)
	update := <-c.segmentUpdateCh
	// "Person 1" was only the provisional label; the real assignment from
	// the accumulated audio gives "Other" here since there's no embedder.
	if update.ID != "seg-9" || update.Text != "refined" || update.Status != "" || update.Speaker != "Other" {
		t.Errorf("update = %+v, want seg-9 finalized with refined text and a real speaker assignment", update)
	}
}

func TestFinishLive_NoOpenLiveSegment(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}})
	var live liveState
	c.finishLive(SourceMic, &live)
	if len(c.refineCh) != 0 {
		t.Errorf("queued %d refinement jobs, want 0", len(c.refineCh))
	}
}

func TestTranscribeSinglePass_BlankResultEmitsNothing(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: " "}}})
	c.handleSegment(SourceMonitor, make([]float32, 8000), nil, &liveState{})

	receiveNone(t, c.segmentCh, "segment for a blank decode")
	if got := c.Stats().MonitorSegments; got != 0 {
		t.Errorf("MonitorSegments = %d, want 0", got)
	}
}

func TestTranscribeSinglePass_EmitsFinalSegment(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "hello", Language: "en"}}, SkipMonitorDiarization: true})
	c.handleSegment(SourceMonitor, make([]float32, 8000), nil, &liveState{})

	seg := <-c.segmentCh
	if seg.Text != "hello" || seg.Status != "" || seg.Speaker != "System Audio" {
		t.Errorf("got %+v, want final \"hello\" from System Audio", seg)
	}
	if got := c.Stats().MonitorSegments; got != 1 {
		t.Errorf("MonitorSegments = %d, want 1", got)
	}
}

// tone is n samples of a square wave at amplitude a (RMS a).
func tone(n int, a float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		if i%2 == 0 {
			s[i] = a
		} else {
			s[i] = -a
		}
	}
	return s
}

func TestHandleSegment_NoiseGateDropsQuietUtterance(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "Yeah."}}, MinSpeechLevelDB: -50})
	live := liveState{shown: "yeah", id: "seg-7", speaker: "You"}
	// -60 dBFS: room noise.
	if !c.dropNoise(SourceMic, tone(8000, 0.001), &mockStreamingSession{}, &live) {
		t.Fatal("noise not dropped")
	}

	update := <-c.segmentUpdateCh
	if update.ID != "seg-7" || update.Status != session.StatusRemoved {
		t.Errorf("update = %+v, want seg-7 removed", update)
	}
	receiveNone(t, c.segmentCh, "segment for noise")
	if len(c.refineCh) != 0 {
		t.Errorf("noise was queued for transcription")
	}
	if live.id != "" {
		t.Errorf("live state not reset")
	}
}

func TestHandleSegment_NoiseGateKeepsSpeech(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "hello"}}, MinSpeechLevelDB: -50})
	var live liveState
	// -20 dBFS: speech.
	if c.dropNoise(SourceMic, tone(8000, 0.1), nil, &live) {
		t.Fatal("speech dropped")
	}
	c.handleSegment(SourceMic, tone(8000, 0.1), nil, &live)
	if seg := <-c.segmentCh; seg.Text != "hello" {
		t.Errorf("speech dropped or changed: %+v", seg)
	}
}

func TestTooQuietMicRelativeToUser(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}, MinSpeechLevelDB: -50, MicLevelMarginDB: 20})
	for i := 0; i < 10; i++ {
		if c.tooQuiet(SourceMic, -18) {
			t.Fatal("the user's own level dropped")
		}
	}
	if !c.tooQuiet(SourceMic, -45) {
		t.Error("faint mic speech 27 dB below the user kept")
	}
	if c.tooQuiet(SourceMic, -30) {
		t.Error("mic speech 12 dB below the user dropped")
	}
	if c.tooQuiet(SourceMonitor, -45) {
		t.Error("the margin applied to remote audio")
	}
	if !c.tooQuiet(SourceMonitor, -55) {
		t.Error("remote audio below the floor kept")
	}
}

func TestTurnMode(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "words"}}
	changeAt := 30.0
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30,
		SpeakerChanged: func(from, to float64) bool { return from <= changeAt && changeAt <= to }})
	utter := func(src SourceType, s, e float64) { c.addToTurn(src, make([]float32, int((e-s)*16000)), s, e) }
	drain := func() (n int, spans [][2]float64) {
		for {
			select {
			case seg := <-c.segmentCh:
				n++
				spans = append(spans, [2]float64{seg.StartTime, seg.EndTime})
			default:
				return
			}
		}
	}
	utter(SourceMonitor, 0, 3)
	utter(SourceMonitor, 3.6, 6) // short pause: same turn
	if n, _ := drain(); n != 0 {
		t.Fatalf("a turn was sent before it ended (%d)", n)
	}
	utter(SourceMic, 7, 8) // the other side speaks: the remote turn ends
	if n, spans := drain(); n != 1 || spans[0] != [2]float64{0, 6} {
		t.Fatalf("after the mic spoke: %d lines %v, want one 0-6", n, spans)
	}
	utter(SourceMonitor, 9, 12) // mic turn ends
	if n, _ := drain(); n != 1 {
		t.Fatalf("mic turn not sent when the call spoke")
	}
	utter(SourceMonitor, 30.2, 33) // a new voice signaled at 30: new turn
	if n, spans := drain(); n != 1 || spans[0] != [2]float64{9, 12} {
		t.Fatalf("speaker change: %d lines %v, want 9-12 sent", n, spans)
	}
	utter(SourceMonitor, 36, 38) // pause over 2 s: new turn
	if n, _ := drain(); n != 1 {
		t.Fatalf("long pause did not end the turn")
	}
	c.flushTurn(SourceMonitor)
	if n, _ := drain(); n != 1 {
		t.Fatalf("open turn not sent at the end")
	}
}

func TestTurnModeTwoPass(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "the whole turn"}}
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30})
	sess := &mockStreamingSession{}
	// Two utterances, each with a pass-1 line showing.
	first := liveState{partial: "the whole", id: "seg-1", speaker: "Person 1"}
	c.handleSegment(SourceMonitor, make([]float32, 16000), sess, &first)
	second := liveState{partial: "turn", id: "seg-2", speaker: "Person 1"}
	c.handleSegment(SourceMonitor, make([]float32, 16000), sess, &second)
	for i := 0; i < 2; i++ {
		if u := <-c.segmentUpdateCh; u.Status != "pending" {
			t.Fatalf("pass-1 line %d not shown as pending: %+v", i, u)
		}
	}
	if len(c.refineCh) != 0 {
		t.Fatal("an utterance was refined before its turn ended")
	}
	c.flushTurn(SourceMonitor)
	runRefineWorker(c)
	final := <-c.segmentUpdateCh
	if final.ID != "seg-1" || final.Text != "the whole turn" || final.Status != "" {
		t.Errorf("turn line = %+v, want seg-1 final with the turn's text", final)
	}
	if gone := <-c.segmentUpdateCh; gone.ID != "seg-2" || gone.Status != session.StatusRemoved {
		t.Errorf("absorbed line = %+v, want seg-2 removed", gone)
	}
}

func TestTurnInterjection(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "words"}}
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30, TurnInterjection: 1})
	utter := func(src SourceType, s, e float64) { c.addToTurn(src, make([]float32, int((e-s)*16000)), s, e) }
	drain := func() (spans [][2]float64) {
		for {
			select {
			case seg := <-c.segmentCh:
				spans = append(spans, [2]float64{seg.StartTime, seg.EndTime})
			default:
				return
			}
		}
	}
	utter(SourceMic, 0, 4)
	utter(SourceMonitor, 4.2, 4.8) // "mm": the mic's turn goes on
	utter(SourceMic, 5, 9)         // the mic goes on: only the interjection is sent
	if spans := drain(); len(spans) != 1 || spans[0] != [2]float64{4.2, 4.8} {
		t.Fatalf("after the mic went on: %v, want just the interjection", spans)
	}
	utter(SourceMonitor, 10, 14) // a real reply ends the mic's turn
	if spans := drain(); len(spans) != 1 || spans[0] != [2]float64{0, 9} {
		t.Fatalf("after a real reply: %v, want the mic's 0-9 turn", spans)
	}
}
