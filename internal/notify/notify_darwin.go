package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// darwinNotifier sends notifications via System Events (osascript) — the
// macOS equivalent of notify-send. Uses `display notification`, which
// (unlike `keystroke`) needs no Accessibility permission.
type darwinNotifier struct {
	appName string
}

// NewNotifier creates a Notifier for macOS.
func NewNotifier() Notifier {
	return &darwinNotifier{appName: "Tomoe"}
}

func (n *darwinNotifier) Send(title, body string) error {
	script := fmt.Sprintf(
		`display notification %s with title %s`,
		appleScriptQuote(body), appleScriptQuote(title),
	)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript display notification: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appleScriptQuote escapes `text` for safe interpolation into an
// AppleScript double-quoted string literal.
func appleScriptQuote(text string) string {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
