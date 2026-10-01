package daemon

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// --- silenceTimeout tests ---

func TestSilenceTimeoutDefault(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			Output: config.OutputConfig{
				SilenceTimeout: 0,
			},
		},
	}

	got := d.silenceTimeout()
	want := 5 * time.Second
	if got != want {
		t.Errorf("silenceTimeout() with 0 config = %v, want %v", got, want)
	}
}

func TestSilenceTimeoutNegative(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			Output: config.OutputConfig{
				SilenceTimeout: -3.0,
			},
		},
	}

	got := d.silenceTimeout()
	want := 5 * time.Second
	if got != want {
		t.Errorf("silenceTimeout() with negative config = %v, want %v", got, want)
	}
}

func TestSilenceTimeoutCustom(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			Output: config.OutputConfig{
				SilenceTimeout: 10.0,
			},
		},
	}

	got := d.silenceTimeout()
	want := 10 * time.Second
	if got != want {
		t.Errorf("silenceTimeout() with 10.0 config = %v, want %v", got, want)
	}
}

func TestSilenceTimeoutFractional(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			Output: config.OutputConfig{
				SilenceTimeout: 2.5,
			},
		},
	}

	got := d.silenceTimeout()
	want := 2500 * time.Millisecond
	if got != want {
		t.Errorf("silenceTimeout() with 2.5 config = %v, want %v", got, want)
	}
}

// --- formatTimestamp tests ---

func TestFormatTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		seconds float64
		want    string
	}{
		{"zero", 0, "00:00"},
		{"one second", 1.0, "00:01"},
		{"thirty seconds", 30.0, "00:30"},
		{"one minute", 60.0, "01:00"},
		{"one minute five seconds", 65.0, "01:05"},
		{"fractional rounds down", 65.5, "01:05"},
		{"one hour", 3600.0, "60:00"},
		{"ninety minutes", 5400.0, "90:00"},
		{"sub-second", 0.9, "00:00"},
		{"just under a minute", 59.9, "00:59"},
		{"large value", 7261.0, "121:01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimestamp(tt.seconds)
			if got != tt.want {
				t.Errorf("formatTimestamp(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}

// --- MeetingOpts / New tests ---

func TestNewWithNilOpts(t *testing.T) {
	cfg := &config.Config{}
	d := New(cfg, nil, nil, nil)

	if d.cfg != cfg {
		t.Error("New() did not store cfg")
	}
	if d.engines != nil {
		t.Error("New() engines should be nil")
	}
	if d.svc != nil {
		t.Error("New() svc should be nil")
	}
	if d.meetingHotkey != nil {
		t.Error("New(nil opts) meetingHotkey should be nil")
	}
	if d.embedders != nil {
		t.Error("New(nil opts) embedders should be nil")
	}
	if d.tracker != nil {
		t.Error("New(nil opts) tracker should be nil")
	}
	if d.store != nil {
		t.Error("New(nil opts) store should be nil")
	}
	if d.modelStatus != nil {
		t.Error("New(nil opts) modelStatus should be nil")
	}
}

func TestNewWithMeetingOpts(t *testing.T) {
	cfg := &config.Config{}
	store := session.NewStore(t.TempDir())
	modelStatus := &models.Status{
		ParakeetReady: true,
		VADReady:      true,
		ModelDir:      "/fake/models",
		VADPath:       "/fake/models/silero_vad.onnx",
	}

	opts := &MeetingOpts{
		Store:       store,
		ModelStatus: modelStatus,
	}

	d := New(cfg, nil, nil, opts)

	if d.store != store {
		t.Error("New(opts) did not copy Store")
	}
	if d.modelStatus != modelStatus {
		t.Error("New(opts) did not copy ModelStatus")
	}
	if d.meetingHotkey != nil {
		t.Error("New(opts) meetingHotkey should be nil when not provided in opts")
	}
	if d.embedders != nil {
		t.Error("New(opts) embedders should be nil when not provided in opts")
	}
	if d.tracker != nil {
		t.Error("New(opts) tracker should be nil when not provided in opts")
	}
}

// --- cleanupAll tests ---

func TestCleanupAllNilBoth(t *testing.T) {
	cfg := &config.Config{}
	d := New(cfg, nil, nil, nil)

	// Should not panic with both nil.
	d.cleanupAll(nil, nil)
}

func TestCleanupAllNilDict(t *testing.T) {
	cfg := &config.Config{}
	d := New(cfg, nil, nil, nil)

	// Should not panic when only dict is nil.
	d.cleanupAll(nil, nil)
}

func TestCleanupAllNilMeet(t *testing.T) {
	cfg := &config.Config{}
	d := New(cfg, nil, nil, nil)

	// Should not panic when only meet is nil.
	d.cleanupAll(nil, nil)
}
