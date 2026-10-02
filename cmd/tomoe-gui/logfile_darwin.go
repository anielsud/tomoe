//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
)

// maxLogBytes is where the log rolls over to tomoe-gui.log.1.
const maxLogBytes = 20 << 20

// setupLogFile sends the app's output to ~/Library/Logs/Tomoe/tomoe-gui.log:
// launched from the Dock, stdout and stderr go nowhere, so a crash's stack
// trace (a Go panic leaves no macOS crash report) would be lost. The file
// descriptors themselves are redirected, so native code's output and the
// Go runtime's fatal errors land there too. A log over 20 MB is kept as
// tomoe-gui.log.1 and a new one started.
func setupLogFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, "Library", "Logs", "Tomoe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "tomoe-gui.log")
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_ = syscall.Dup2(int(f.Fd()), 1)
	_ = syscall.Dup2(int(f.Fd()), 2)
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	fmt.Printf("\n===== Tomoe started %s (pid %d) =====\n", time.Now().Format(time.RFC3339), os.Getpid())
}
