package backend

import (
	"context"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// videoHintActivityCap bounds the in-memory ring buffer so a long
// meeting's activity trace can't grow unbounded — only recent history
// needs to be replayable to a panel opened mid-session; older entries
// were already pushed live via the "videohint:activity" event as they
// happened.
const videoHintActivityCap = 50

// VideoHintActivityEntry is the camelCase JSON view of a videohint.Event
// for the frontend's live activity log — same reasoning as
// VideoHintSummary elsewhere in this package: Go's exported struct
// fields would otherwise serialize as PascalCase.
type VideoHintActivityEntry struct {
	Time     string `json:"time"`
	Platform string `json:"platform"`
	Stage    string `json:"stage"`
	Detail   string `json:"detail"`
	Name     string `json:"name,omitempty"`
}

func toVideoHintActivityEntry(ev videohint.Event) VideoHintActivityEntry {
	return VideoHintActivityEntry{
		Time:     ev.Time.Format(time.RFC3339),
		Platform: string(ev.Platform),
		Stage:    string(ev.Stage),
		Detail:   ev.Detail,
		Name:     ev.Name,
	}
}

// emitVideoHintEvents drains one meeting session's videohint.Poll
// events until ctx is cancelled: buffers a short recent-activity ring
// (so GetVideoHintActivity can backfill a panel opened mid-session),
// attaches any recognized name to the speaker tracker, and forwards
// every event to the frontend live as it happens.
func (a *App) emitVideoHintEvents(ctx context.Context, events chan videohint.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}

			a.videoHintMu.Lock()
			a.videoHintActivity = append(a.videoHintActivity, ev)
			if len(a.videoHintActivity) > videoHintActivityCap {
				a.videoHintActivity = a.videoHintActivity[len(a.videoHintActivity)-videoHintActivityCap:]
			}
			a.videoHintMu.Unlock()

			if ev.Stage == videohint.StageOCRHit && ev.Name != "" && a.tracker != nil {
				a.tracker.SetHintForRecent(ev.Name, videohint.HintAttachMaxAge)
			}

			wailsRuntime.EventsEmit(a.ctx, "videohint:activity", toVideoHintActivityEntry(ev))
		}
	}
}

// GetVideoHintActivity returns the current session's most recent
// video-hint activity entries (oldest first), so a panel opened
// mid-session can backfill history instead of only showing events from
// the moment it mounted.
func (a *App) GetVideoHintActivity() []VideoHintActivityEntry {
	a.videoHintMu.Lock()
	defer a.videoHintMu.Unlock()

	out := make([]VideoHintActivityEntry, len(a.videoHintActivity))
	for i, ev := range a.videoHintActivity {
		out[i] = toVideoHintActivityEntry(ev)
	}
	return out
}
