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
//
// The descriptors point at a pipe whose reader stamps each line with the
// time, so the log says when a meeting was detected or a save started. A
// crash report goes to the file directly (SetCrashOutput), so it doesn't
// depend on the reader outliving the crash. Without a pipe, output goes to
// the file unstamped, as before.
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
	out := int(f.Fd())
	if pr, pw, err := os.Pipe(); err == nil {
		go stampLines(pr, f, time.Now)
		out = int(pw.Fd())
		defer pw.Close() // descriptors 1 and 2 keep the pipe open
	}
	_ = syscall.Dup2(out, 1)
	_ = syscall.Dup2(out, 2)
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	logFile = f
	fmt.Printf("\n===== Tomoe started %s (pid %d) =====\n", time.Now().Format(time.RFC3339), os.Getpid())
}

// logFile is the open log, for logStopped.
var logFile *os.File

// logStopped marks a clean quit in the log. A start banner with no stop
// after it means the app died, which is what packaging/macos's relaunch
// agent looks for. It writes to the file directly and syncs: the process
// may end before the stamping reader gets to anything sent through the
// pipe.
func logStopped() {
	if logFile == nil {
		return
	}
	now := time.Now()
	fmt.Fprintf(logFile, "%s===== Tomoe stopped %s (pid %d) =====\n", now.Format(stampFormat), now.Format(time.RFC3339), os.Getpid())
	_ = logFile.Sync()
}
