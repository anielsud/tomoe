package videohint

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
)

// SnapshotMeta describes one escalation snapshot: enough structured
// information to write a real Rule for this platform's UI later,
// without needing to reproduce the exact moment it was captured.
type SnapshotMeta struct {
	Platform    meeting.Platform `json:"platform"`
	WindowTitle string           `json:"window_title"`
	WindowOwner string           `json:"window_owner"`
	Width       int              `json:"width"`
	Height      int              `json:"height"`
	Timestamp   time.Time        `json:"timestamp"`
	// Reason is why this escalated instead of producing a hint, e.g.
	// "no rule configured for this platform" or "no ring match found".
	Reason string `json:"reason"`
}

// CaptureSnapshot writes a PNG of the given RGB pixel buffer (packed,
// no padding, 3 bytes per pixel) plus a metadata.json sidecar under
// dir/<platform>-<unixts>-<shortid>/, so a real Rule can be written for
// this platform's UI later instead of the escalation only ever being
// logged and forgotten. dir is an explicit parameter (rather than
// always using the global default) so this is testable against a temp
// directory — see CaptureUnrecognizedUI for the default-location
// wrapper real callers use.
func CaptureSnapshot(dir string, meta SnapshotMeta, pix []byte, width, height int) error {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return fmt.Errorf("videohint: pixel buffer too small for %dx%d RGB", width, height)
	}

	id := fmt.Sprintf("%s-%d-%s", string(meta.Platform), meta.Timestamp.Unix(), uuid.NewString()[:8])
	snapDir := filepath.Join(dir, id)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return fmt.Errorf("videohint: creating snapshot dir: %w", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 3
			img.Set(x, y, color.RGBA{R: pix[i], G: pix[i+1], B: pix[i+2], A: 255})
		}
	}

	f, err := os.Create(filepath.Join(snapDir, "frame.png"))
	if err != nil {
		return fmt.Errorf("videohint: creating frame.png: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("videohint: encoding frame.png: %w", err)
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("videohint: marshaling metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "metadata.json"), metaBytes, 0o644); err != nil {
		return fmt.Errorf("videohint: writing metadata.json: %w", err)
	}

	return nil
}

// CaptureUnrecognizedUI is CaptureSnapshot using the default staging
// location, config.UnrecognizedUIPendingDir() — what real (non-test)
// callers use. Deliberately the *pending* dir, not the approved
// library: a captured window can be the wrong thing entirely (see
// ApproveSnapshot's doc comment), so nothing lands anywhere durable
// without a human reviewing it first.
func CaptureUnrecognizedUI(meta SnapshotMeta, pix []byte, width, height int) error {
	return CaptureSnapshot(config.UnrecognizedUIPendingDir(), meta, pix, width, height)
}

// PendingSnapshot summarizes one snapshot awaiting review.
type PendingSnapshot struct {
	ID   string
	Meta SnapshotMeta
}

// ListSnapshots lists snapshots in dir awaiting review, newest first.
// Entries with an unreadable or malformed metadata.json are skipped
// rather than failing the whole listing.
func ListSnapshots(dir string) ([]PendingSnapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("videohint: reading %s: %w", dir, err)
	}

	var out []PendingSnapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var meta SnapshotMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		out = append(out, PendingSnapshot{ID: e.Name(), Meta: meta})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Meta.Timestamp.After(out[j].Meta.Timestamp) })
	return out, nil
}

// ApproveSnapshot moves a pending snapshot from pendingDir into
// approvedDir — the *only* way anything reaches the permanent
// escalation library a real Rule ever gets calibrated from. Capturing
// a snapshot is not the same as it being safe or relevant: the window
// FindMeetingWindow-style matching finds can be the wrong thing
// entirely (e.g. a chat tab that happens to be open, not an actual
// call), so nothing is promoted automatically.
func ApproveSnapshot(pendingDir, approvedDir, id string) error {
	src := filepath.Join(pendingDir, id)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("videohint: pending snapshot %q not found: %w", id, err)
	}
	if err := os.MkdirAll(approvedDir, 0o755); err != nil {
		return fmt.Errorf("videohint: creating approved dir: %w", err)
	}
	if err := os.Rename(src, filepath.Join(approvedDir, id)); err != nil {
		return fmt.Errorf("videohint: approving snapshot %q: %w", id, err)
	}
	return nil
}

// DiscardSnapshot permanently deletes a pending snapshot without
// approving it.
func DiscardSnapshot(pendingDir, id string) error {
	src := filepath.Join(pendingDir, id)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("videohint: pending snapshot %q not found: %w", id, err)
	}
	return os.RemoveAll(src)
}

// ListPendingUnrecognizedUIs, ApproveUnrecognizedUI, and
// DiscardUnrecognizedUI are the above three operations against the
// default pending/approved locations — what `tomoe videohint`'s CLI
// commands use.

func ListPendingUnrecognizedUIs() ([]PendingSnapshot, error) {
	return ListSnapshots(config.UnrecognizedUIPendingDir())
}

func ApproveUnrecognizedUI(id string) error {
	return ApproveSnapshot(config.UnrecognizedUIPendingDir(), config.UnrecognizedUIApprovedDir(), id)
}

func DiscardUnrecognizedUI(id string) error {
	return DiscardSnapshot(config.UnrecognizedUIPendingDir(), id)
}
