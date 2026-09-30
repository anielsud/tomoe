//go:build linux

package backend

import (
	"os"
	"path/filepath"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/gpu"
	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
)

// platformTools lists the Linux command-line tools Tomoe shells out to.
func platformTools() []ToolStatus {
	wayland := os.Getenv("XDG_SESSION_TYPE") == "wayland"
	list := []ToolStatus{
		linuxTool("xdotool", "xdotool", "Auto-pasting dictation on X11, and naming the meeting app for browser meetings", !wayland),
		linuxTool("xprop", "x11-utils", "Pasting correctly into terminals", !wayland),
		linuxTool("wtype", "wtype", "Typing dictation on Wayland", wayland),
		linuxTool("notify-send", "libnotify-bin", "Desktop notifications", true),
	}
	return list
}

// linuxTool checks one tool from an apt package. relevant is false when
// the tool isn't needed on this desktop session (X11 vs Wayland), which
// counts as OK when it's missing.
func linuxTool(name, pkg, neededFor string, relevant bool) ToolStatus {
	t := ToolStatus{ID: name, Name: name, Group: groupTools, NeededFor: neededFor}
	if path, err := toolpath.Find(name); err == nil {
		t.OK, t.Detail = true, path
		return t
	}
	if !relevant {
		t.OK, t.Detail = true, "Not installed (not needed in this desktop session)"
		return t
	}
	t.Detail = "Not found"
	t.Fix = commandFix("Copy install command", "sudo apt install "+pkg)
	return t
}

func ffmpegFix() *ToolFix {
	return commandFix("Copy install command", "sudo apt install ffmpeg")
}

func diarizeWorkerFix() *ToolFix {
	return commandFix("Copy install command", "make install   # run in the tomoe source folder")
}

// permissionStatuses is empty on Linux: there are no per-app privacy
// permissions to grant.
func permissionStatuses() []ToolStatus { return nil }

// gpuStatuses checks for an NVIDIA GPU and the CUDA provider libraries
// ONNX Runtime needs to use it (installed by `make install-gpu`).
func gpuStatuses(cfg *config.Config) []ToolStatus {
	info := gpu.Detect()
	t := ToolStatus{ID: "gpu", Name: "NVIDIA GPU (CUDA)", Group: groupGPU, NeededFor: "Faster transcription"}
	if !info.Available {
		t.OK, t.Detail = true, "No NVIDIA GPU found: transcription runs on the CPU"
		return []ToolStatus{t}
	}
	libs := filepath.Join(config.LibDir(), "libonnxruntime_providers_cuda.so")
	if _, err := os.Stat(libs); err != nil {
		t.Detail = info.Name + " found, but the CUDA libraries aren't installed"
		t.Fix = commandFix("Copy install command", "make install-gpu   # run in the tomoe source folder")
		return []ToolStatus{t}
	}
	t.OK = true
	t.Detail = info.Name
	if !cfg.Transcription.GPUEnabled {
		t.Detail += " · ready, but GPU is off in Settings"
	}
	return []ToolStatus{t}
}

// platformFix has no Linux "action" fixes beyond model downloads: the
// rest need sudo, so they're offered as commands to copy.
func platformFix(*App, string) func() (string, error) { return nil }
