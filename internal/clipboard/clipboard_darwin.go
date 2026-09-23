package clipboard

import (
	"fmt"
	"os/exec"
	"strings"

	atotto "github.com/atotto/clipboard"
)

// darwinWriter implements Writer for macOS.
type darwinWriter struct{}

// NewWriter creates a clipboard Writer for macOS.
func NewWriter() Writer {
	return &darwinWriter{}
}

func (w *darwinWriter) Write(text string) error {
	// atotto/clipboard already shells out to pbcopy/pbpaste on macOS —
	// no platform branching needed here, unlike Linux's X11/Wayland split.
	return atotto.WriteAll(text)
}

// TypeText simulates keyboard input via System Events' `keystroke` command
// (osascript) — the macOS equivalent of xdotool type/wtype. Requires the
// calling process to have Accessibility permission granted (System
// Settings → Privacy & Security → Accessibility), same category of
// permission tomoe-darwin's Python spike already needed for screen
// capture; there is no macOS equivalent of "no permission needed" here.
func (w *darwinWriter) TypeText(text string) error {
	if _, err := exec.LookPath("osascript"); err != nil {
		return fmt.Errorf("osascript not found (unexpected on macOS)")
	}
	script := fmt.Sprintf(
		`tell application "System Events" to keystroke %s`,
		appleScriptQuote(text),
	)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript keystroke: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appleScriptQuote escapes `text` for safe interpolation into an
// AppleScript double-quoted string literal (backslash and quote
// characters only — AppleScript has no other special-casing here).
func appleScriptQuote(text string) string {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
