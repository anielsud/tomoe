package meeting

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// getWindowTitleByPID uses xdotool to find window titles for a given PID.
// Returns the first matching window title, or "" if none found.
// On Wayland or if xdotool is not available, returns "".
func getWindowTitleByPID(pid int) string {
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		return "" // xdotool not available (Wayland or not installed)
	}

	// Search for windows belonging to this PID
	out, err := exec.Command(xdotool, "search", "--pid", fmt.Sprintf("%d", pid), "--name", ".").Output()
	if err != nil {
		return ""
	}

	// xdotool search returns window IDs, one per line
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, wid := range lines {
		wid = strings.TrimSpace(wid)
		if wid == "" {
			continue
		}
		// Get the window name for this ID
		nameOut, err := exec.Command(xdotool, "getwindowname", wid).Output()
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameOut))
		if name != "" {
			return name
		}
	}

	return ""
}

// processExists checks if a process with the given PID is still running.
func processExists(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}
