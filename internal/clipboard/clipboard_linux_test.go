package clipboard

import (
	"os"
	"strings"
	"testing"
)

func TestLinuxWriterDisplayServer(t *testing.T) {
	orig := os.Getenv("XDG_SESSION_TYPE")
	defer func() { _ = os.Setenv("XDG_SESSION_TYPE", orig) }()

	_ = os.Setenv("XDG_SESSION_TYPE", "x11")
	w := NewWriter()
	lw, ok := w.(*linuxWriter)
	if !ok {
		t.Fatal("NewWriter() did not return *linuxWriter")
	}
	if lw.displayServer != "x11" {
		t.Errorf("displayServer = %q, want %q", lw.displayServer, "x11")
	}
}

func TestTypeTextUnsupportedDisplayServer(t *testing.T) {
	w := &linuxWriter{displayServer: "unknown"}
	err := w.TypeText("hello")
	if err == nil {
		t.Fatal("TypeText() on unknown display server should return error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want it to contain 'not supported'", err.Error())
	}
}

func TestTypeTextX11DisplayServer(t *testing.T) {
	w := &linuxWriter{displayServer: "x11"}
	err := w.TypeText("test")
	// On CI without X11/xdotool this will error, but the routing should work
	if err != nil && !strings.Contains(err.Error(), "xdotool") {
		t.Errorf("unexpected error for x11: %v", err)
	}
}

func TestTypeTextWaylandDisplayServer(t *testing.T) {
	w := &linuxWriter{displayServer: "wayland"}
	err := w.TypeText("test")
	// On CI without wtype this will error, but the routing should work
	if err != nil && !strings.Contains(err.Error(), "wtype") {
		t.Errorf("unexpected error for wayland: %v", err)
	}
}

func TestTypeTextEmptyString(t *testing.T) {
	w := &linuxWriter{displayServer: "unknown"}
	err := w.TypeText("")
	if err == nil {
		t.Fatal("TypeText('') on unknown display server should still return error")
	}
}
