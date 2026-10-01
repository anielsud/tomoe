package backend

import (
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// emitSessionSegments applies a coordinator's segments and pass-2
// refinements (its Segments and SegmentUpdates channels) to sess and
// forwards them to the frontend, returning a channel closed once both
// are drained.
//
// It is bound to one session and one coordinator rather than reading
// a.currentSess: StopSession clears a.currentSess right away, but the
// coordinator keeps delivering refinements for jobs still queued at stop,
// and those belong in the session being saved (persistSession waits on
// the returned channel). Reading a.currentSess instead would drop them,
// or, once the next session has started, apply them to its segments,
// since segment IDs restart at seg-1 for every coordinator.
func (a *App) emitSessionSegments(segments, updates <-chan session.Segment, sess *session.Session, md *diarize.SessionDiarizer) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for seg := range segments {
			a.applySegment(sess, md, seg, "transcript:segment")
		}
	}()
	// Refinements arrive as a distinct event, since the frontend needs to
	// update a line in place rather than append a new one. Only fires
	// anything when the session was started with two-pass transcription
	// enabled — see live.Config.StreamingEngine.
	go func() {
		defer wg.Done()
		for seg := range updates {
			a.applySegment(sess, md, seg, "transcript:segment:update")
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// applySegment upserts seg into sess (a new segment and a revision of it
// travel on different channels, so either may arrive first) and forwards
// it to the frontend -- unless another session has started since, whose
// transcript the frontend is now showing.
//
// When the session is diarized during the meeting (md non-nil), seg is
// relabeled from the latest timeline first (see diarize.SessionDiarizer).
func (a *App) applySegment(sess *session.Session, md *diarize.SessionDiarizer, seg session.Segment, event string) {
	a.mu.Lock()
	if md != nil {
		md.LabelNewLocked(&seg)
	}
	sess.UpsertSegment(seg)
	visible := a.currentSess == nil || a.currentSess == sess
	a.mu.Unlock()

	if visible {
		wailsRuntime.EventsEmit(a.ctx, event, seg)
	}
}
