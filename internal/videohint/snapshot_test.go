package videohint

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
)

func TestCaptureSnapshot_WritesFrameAndMetadata(t *testing.T) {
	dir := t.TempDir()
	const width, height = 10, 8
	pix := make([]byte, width*height*3)
	for i := range pix {
		pix[i] = byte(i % 256)
	}

	meta := SnapshotMeta{
		Platform:    meeting.PlatformTeams,
		WindowTitle: "Test Meeting",
		WindowOwner: "Microsoft Teams",
		Width:       width,
		Height:      height,
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Reason:      "no rule configured for this platform",
	}

	if err := CaptureSnapshot(dir, meta, pix, width, height); err != nil {
		t.Fatalf("CaptureSnapshot() error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 snapshot dir, got %d", len(entries))
	}
	snapDir := filepath.Join(dir, entries[0].Name())

	// frame.png should decode back to the same dimensions.
	f, err := os.Open(filepath.Join(snapDir, "frame.png"))
	if err != nil {
		t.Fatalf("opening frame.png: %v", err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decoding frame.png: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, width, height) {
		t.Errorf("frame.png bounds = %v, want %v", img.Bounds(), image.Rect(0, 0, width, height))
	}
	// Spot-check one pixel round-trips correctly.
	r, g, b, _ := img.At(1, 0).RGBA()
	wantIdx := (0*width + 1) * 3
	if uint8(r>>8) != pix[wantIdx] || uint8(g>>8) != pix[wantIdx+1] || uint8(b>>8) != pix[wantIdx+2] {
		t.Errorf("pixel (1,0) = (%d,%d,%d), want (%d,%d,%d)", r>>8, g>>8, b>>8, pix[wantIdx], pix[wantIdx+1], pix[wantIdx+2])
	}

	// metadata.json should round-trip.
	metaBytes, err := os.ReadFile(filepath.Join(snapDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	var gotMeta SnapshotMeta
	if err := json.Unmarshal(metaBytes, &gotMeta); err != nil {
		t.Fatalf("unmarshaling metadata.json: %v", err)
	}
	if gotMeta != meta {
		t.Errorf("metadata round-trip = %+v, want %+v", gotMeta, meta)
	}
}

func TestCaptureSnapshot_RejectsUndersizedBuffer(t *testing.T) {
	dir := t.TempDir()
	meta := SnapshotMeta{Platform: meeting.PlatformZoom, Timestamp: time.Now()}
	err := CaptureSnapshot(dir, meta, []byte{1, 2, 3}, 10, 10)
	if err == nil {
		t.Fatal("CaptureSnapshot() with undersized buffer should return an error")
	}
}

func TestCaptureSnapshot_MultipleCallsDontCollide(t *testing.T) {
	dir := t.TempDir()
	meta := SnapshotMeta{Platform: meeting.PlatformSlack, Timestamp: time.Now()}
	pix := make([]byte, 4*4*3)

	if err := CaptureSnapshot(dir, meta, pix, 4, 4); err != nil {
		t.Fatalf("first CaptureSnapshot() error: %v", err)
	}
	if err := CaptureSnapshot(dir, meta, pix, 4, 4); err != nil {
		t.Fatalf("second CaptureSnapshot() error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 distinct snapshot dirs, got %d", len(entries))
	}
}

func TestListSnapshots(t *testing.T) {
	t.Run("empty/missing dir returns no error", func(t *testing.T) {
		got, err := ListSnapshots(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("ListSnapshots() error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d entries, want 0", len(got))
		}
	})

	t.Run("lists captured snapshots newest first", func(t *testing.T) {
		dir := t.TempDir()
		pix := make([]byte, 4*4*3)

		older := SnapshotMeta{Platform: meeting.PlatformTeams, Timestamp: time.Now().Add(-time.Hour)}
		newer := SnapshotMeta{Platform: meeting.PlatformZoom, Timestamp: time.Now()}

		if err := CaptureSnapshot(dir, older, pix, 4, 4); err != nil {
			t.Fatalf("CaptureSnapshot(older) error: %v", err)
		}
		if err := CaptureSnapshot(dir, newer, pix, 4, 4); err != nil {
			t.Fatalf("CaptureSnapshot(newer) error: %v", err)
		}

		got, err := ListSnapshots(dir)
		if err != nil {
			t.Fatalf("ListSnapshots() error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2", len(got))
		}
		if got[0].Meta.Platform != meeting.PlatformZoom {
			t.Errorf("got[0].Meta.Platform = %v, want %v (newest first)", got[0].Meta.Platform, meeting.PlatformZoom)
		}
		if got[1].Meta.Platform != meeting.PlatformTeams {
			t.Errorf("got[1].Meta.Platform = %v, want %v", got[1].Meta.Platform, meeting.PlatformTeams)
		}
	})

	t.Run("skips entries with malformed metadata", func(t *testing.T) {
		dir := t.TempDir()
		badDir := filepath.Join(dir, "bad-entry")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error: %v", err)
		}
		if err := os.WriteFile(filepath.Join(badDir, "metadata.json"), []byte("not json"), 0o644); err != nil {
			t.Fatalf("WriteFile() error: %v", err)
		}

		got, err := ListSnapshots(dir)
		if err != nil {
			t.Fatalf("ListSnapshots() error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d entries, want 0 (malformed entry should be skipped)", len(got))
		}
	})
}

func TestApproveSnapshot(t *testing.T) {
	pendingDir := t.TempDir()
	approvedDir := filepath.Join(t.TempDir(), "approved") // doesn't exist yet
	pix := make([]byte, 4*4*3)
	meta := SnapshotMeta{Platform: meeting.PlatformTeams, Timestamp: time.Now()}

	if err := CaptureSnapshot(pendingDir, meta, pix, 4, 4); err != nil {
		t.Fatalf("CaptureSnapshot() error: %v", err)
	}
	pending, err := ListSnapshots(pendingDir)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListSnapshots() = %v, %v, want 1 entry", pending, err)
	}
	id := pending[0].ID

	if err := ApproveSnapshot(pendingDir, approvedDir, id); err != nil {
		t.Fatalf("ApproveSnapshot() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pendingDir, id)); !os.IsNotExist(err) {
		t.Errorf("pending dir still has %q after approval, want it moved", id)
	}
	if _, err := os.Stat(filepath.Join(approvedDir, id, "metadata.json")); err != nil {
		t.Errorf("approved dir missing %q's metadata.json: %v", id, err)
	}
}

func TestApproveSnapshot_NotFound(t *testing.T) {
	pendingDir := t.TempDir()
	approvedDir := t.TempDir()
	if err := ApproveSnapshot(pendingDir, approvedDir, "does-not-exist"); err == nil {
		t.Error("ApproveSnapshot() with a nonexistent id should return an error")
	}
}

func TestDiscardSnapshot(t *testing.T) {
	pendingDir := t.TempDir()
	pix := make([]byte, 4*4*3)
	meta := SnapshotMeta{Platform: meeting.PlatformSlack, Timestamp: time.Now()}

	if err := CaptureSnapshot(pendingDir, meta, pix, 4, 4); err != nil {
		t.Fatalf("CaptureSnapshot() error: %v", err)
	}
	pending, err := ListSnapshots(pendingDir)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListSnapshots() = %v, %v, want 1 entry", pending, err)
	}
	id := pending[0].ID

	if err := DiscardSnapshot(pendingDir, id); err != nil {
		t.Fatalf("DiscardSnapshot() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pendingDir, id)); !os.IsNotExist(err) {
		t.Errorf("pending dir still has %q after discard, want it removed", id)
	}
}

func TestDiscardSnapshot_NotFound(t *testing.T) {
	pendingDir := t.TempDir()
	if err := DiscardSnapshot(pendingDir, "does-not-exist"); err == nil {
		t.Error("DiscardSnapshot() with a nonexistent id should return an error")
	}
}
