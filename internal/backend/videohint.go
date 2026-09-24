package backend

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

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
