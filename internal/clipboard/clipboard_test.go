package clipboard

import (
	"os"
	"testing"
)

func TestDisplayServer(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"x11", "x11", "x11"},
		{"wayland", "wayland", "wayland"},
		{"empty", "", "unknown"},
		{"tty", "tty", "tty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := os.Getenv("XDG_SESSION_TYPE")
			defer func() { _ = os.Setenv("XDG_SESSION_TYPE", orig) }()

			_ = os.Setenv("XDG_SESSION_TYPE", tt.env)
			got := DisplayServer()
			if got != tt.want {
				t.Errorf("DisplayServer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewWriterImplementsInterface(t *testing.T) {
	w := NewWriter()
	if w == nil {
		t.Fatal("NewWriter() returned nil")
	}
}
