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
