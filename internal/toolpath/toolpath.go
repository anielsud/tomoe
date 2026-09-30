// Package toolpath finds external command-line tools (ffmpeg) that Tomoe
// shells out to.
//
// exec.LookPath alone isn't enough on macOS: an app launched from Finder or
// the Dock gets launchd's minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin), not
// the user's shell PATH, so a Homebrew-installed ffmpeg in /opt/homebrew/bin
// is invisible to Tomoe.app even though `tomoe` in a terminal finds it.
// Before this, every session recorded from Tomoe.app silently saved no audio.
package toolpath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// fallbackDirs are checked, in order, when a tool isn't on PATH: Homebrew
// on Apple silicon, Homebrew on Intel (and the usual manual-install
// location), then MacPorts. Harmless on Linux, where the tools live on
// PATH in /usr/bin anyway.
var fallbackDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}

// Find returns the path to the named tool: from PATH if it's there,
// otherwise from the first fallback directory that has it as an
// executable file.
func Find(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return findIn(name, fallbackDirs)
}

func findIn(name string, dirs []string) (string, error) {
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH or in %v: install %s", name, dirs, name)
}

// FFmpeg returns the path to ffmpeg; see Find.
func FFmpeg() (string, error) {
	return Find("ffmpeg")
}
