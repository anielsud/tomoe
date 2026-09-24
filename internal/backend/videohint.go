package backend

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
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
// VideoHintSummary below: Go's exported struct fields would otherwise
// serialize as PascalCase.
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

// VideoHintSummary is a JSON-friendly view of a pending videohint
// snapshot for the GUI's review panel — decoupled from
// videohint.SnapshotMeta's own json tags so this API's shape is ours
// to control (camelCase, matching this frontend's other bound-method
// responses) rather than whatever internal/videohint happens to use.
type VideoHintSummary struct {
	ID          string `json:"id"`
	Platform    string `json:"platform"`
	WindowTitle string `json:"windowTitle"`
	WindowOwner string `json:"windowOwner"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Timestamp   string `json:"timestamp"` // RFC3339
	Reason      string `json:"reason"`
}

// ListPendingVideoHints returns escalated snapshots awaiting review —
// see internal/videohint's package doc and docs/macos-support.md for
// why these are staged rather than used automatically (the matched
// window can be the wrong thing entirely, e.g. a chat tab, not an
// actual call).
func (a *App) ListPendingVideoHints() ([]VideoHintSummary, error) {
	pending, err := videohint.ListPendingUnrecognizedUIs()
	if err != nil {
		return nil, err
	}

	out := make([]VideoHintSummary, len(pending))
	for i, p := range pending {
		out[i] = VideoHintSummary{
			ID:          p.ID,
			Platform:    string(p.Meta.Platform),
			WindowTitle: p.Meta.WindowTitle,
			WindowOwner: p.Meta.WindowOwner,
			Width:       p.Meta.Width,
			Height:      p.Meta.Height,
			Timestamp:   p.Meta.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			Reason:      p.Meta.Reason,
		}
	}
	return out, nil
}

// GetVideoHintImage returns a pending snapshot's captured frame as a
// data URI, ready to drop straight into an <img src="..."> — Wails
// doesn't expose the local filesystem to the frontend directly, so the
// image has to travel as a bound-method response like everything else.
func (a *App) GetVideoHintImage(id string) (string, error) {
	path := filepath.Join(config.UnrecognizedUIPendingDir(), id, "frame.png")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading snapshot image: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ApproveVideoHint moves a pending snapshot into the permanent
// unrecognized-UI library — the only way anything reaches it; see
// videohint.ApproveSnapshot's doc comment for why.
func (a *App) ApproveVideoHint(id string) error {
	return videohint.ApproveUnrecognizedUI(id)
}

// DiscardVideoHint permanently deletes a pending snapshot without
// approving it.
func (a *App) DiscardVideoHint(id string) error {
	return videohint.DiscardUnrecognizedUI(id)
}
