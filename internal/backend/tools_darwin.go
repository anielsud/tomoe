//go:build darwin

package backend

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
)

// platformTools lists macOS-only command-line dependencies: none, since
// everything else Tomoe shells out to (osascript, open) ships with macOS.
func platformTools() []ToolStatus { return nil }

// ffmpegFix installs ffmpeg with Homebrew when it's there, and otherwise
// points at Homebrew's install command.
func ffmpegFix() *ToolFix {
	if _, err := toolpath.Find("brew"); err == nil {
		return &ToolFix{Kind: "action", Label: "Install with Homebrew"}
	}
	return commandFix("Copy Homebrew install command",
		`/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" && brew install ffmpeg`)
}

func diarizeWorkerFix() *ToolFix {
	return commandFix("Copy rebuild command", "make install-gui-mac   # run in the tomoe source folder")
}

func permissionStatuses() []ToolStatus {
	var list []ToolStatus
	for _, p := range macPermissions {
		t := ToolStatus{ID: p.id, Name: p.name, Group: groupPermissions, NeededFor: p.neededFor}
		if p.granted() {
			t.OK, t.Detail = true, "Granted"
		} else {
			t.Detail = "Not granted"
			if p.restartNote != "" {
				t.Detail += " · " + p.restartNote
			}
			t.Fix = &ToolFix{Kind: "action", Label: "Grant access"}
		}
		list = append(list, t)
	}
	return list
}

func gpuStatuses(*config.Config) []ToolStatus {
	return []ToolStatus{{
		ID: "gpu", Name: "GPU acceleration", Group: groupGPU, OK: true,
		NeededFor: "Faster transcription",
		Detail:    "Not used on macOS: transcription runs on the CPU",
	}}
}

// platformFix returns the macOS "action" fixes: installing ffmpeg with
// Homebrew, and asking for permissions.
func platformFix(a *App, id string) func() (string, error) {
	if id == "ffmpeg" {
		return func() (string, error) {
			brew, err := toolpath.Find("brew")
			if err != nil {
				return "", fmt.Errorf("Homebrew not found")
			}
			cmd := exec.Command(brew, "install", "ffmpeg")
			// A Dock-launched app's PATH lacks Homebrew's own bin folder,
			// which brew's install steps expect.
			cmd.Env = append(os.Environ(), "PATH=/opt/homebrew/bin:/usr/local/bin:"+os.Getenv("PATH"), "HOMEBREW_NO_AUTO_UPDATE=1")
			if err := a.runStreaming(id, cmd); err != nil {
				return "", fmt.Errorf("brew install ffmpeg: %w", err)
			}
			return "", nil
		}
	}
	for _, p := range macPermissions {
		if p.id != id {
			continue
		}
		return func() (string, error) {
			if p.granted() {
				return "", nil
			}
			if !p.request() {
				// macOS won't ask again: the user changes it in Settings.
				if err := openPrivacySettings(p.settingsPane); err != nil {
					return "", fmt.Errorf("opening System Settings: %w", err)
				}
				note := fmt.Sprintf("Turn on Tomoe under %s in System Settings.", p.name)
				if p.restartNote != "" {
					note += " " + p.restartNote + "."
				}
				return note, nil
			}
			return "Answer the macOS prompt, then refresh this page.", nil
		}
	}
	return nil
}
