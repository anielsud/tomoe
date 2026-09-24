//go:build darwin

package meetingaudio

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
	"github.com/sosuke-ai/tomoe-pc/internal/guestaudio"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// NewMonitorSource captures system audio via ScreenCaptureKit, per
// sourceHint (from the frontend's source picker — see
// internal/audiosources.ListActive and internal/backend's
// ListAudioSources/StartSession):
//   - "" (nothing selected): no monitor capture, mic-only.
//   - "everything": the whole system's audio output, not tied to any
//     one app (internal/guestaudio.NewSystemCapturer).
//   - a decimal PID (e.g. "1234", from ListActive): that specific
//     app's audio, via a window it owns. Teams (identified by app
//     name/bundle ID, not process ancestry — see isTeamsPID) resolves
//     via the dedicated teamsvideo.FindMeetingWindow; anything else
//     falls back to teamsvideo.FindWindowForPID's generic PID/window
//     matching. Either way, capture itself is
//     internal/guestaudio.NewWindowCapturer.
//
// Unlike the Linux implementation, failure here is never fatal: no
// window found, a bad/stale PID, and a Tap failing to start (most
// commonly: Screen Recording permission — System Settings > Privacy &
// Security > Screen Recording — hasn't been granted) all just fall
// back to mic-only, which is still a fully-working session. A hard
// failure here would make meeting mode strictly worse than today for
// no benefit.
func NewMonitorSource(sourceHint string) (*audio.StreamCapturer, error) {
	if sourceHint == "" {
		return nil, nil
	}

	var capturer audio.Capturer

	if sourceHint == "everything" {
		capturer = guestaudio.NewSystemCapturer()
	} else {
		pid, err := strconv.ParseInt(sourceHint, 10, 32)
		if err != nil {
			fmt.Printf("meetingaudio: invalid source selection %q, starting mic-only: %v\n", sourceHint, err)
			return nil, nil
		}
		var windowID teamsvideo.WindowID
		if isTeamsPID(int32(pid)) {
			// CoreAudio's audio-active PID for Teams is frequently a
			// short-lived helper/module process (found live: "Microsoft
			// Teams WebView", and later a differently-named "modulehost"
			// helper for a different call) that isn't even a descendant
			// of the main Teams process via the normal parent-PID chain
			// FindWindowForPID walks -- some of these helpers are
			// launchd-spawned XPC services with PID 1 as their parent,
			// not a child of Teams itself, so that walk terminates
			// immediately with "no window found" and silently falls
			// back to mic-only. Once we know from the source's own
			// app identity (not process ancestry) that this is Teams,
			// skip PID/window matching entirely and go straight to the
			// existing, independently-proven title-based finder.
			windowID, err = teamsvideo.FindMeetingWindow()
		} else {
			windowID, err = teamsvideo.FindWindowForPID(int32(pid))
		}
		if err != nil {
			fmt.Printf("meetingaudio: no window found for selected app (PID %d), starting mic-only: %v\n", pid, err)
			return nil, nil
		}
		capturer = guestaudio.NewWindowCapturer(uint32(windowID))
	}

	// Start now, up front, to confirm the tap actually works (most
	// commonly: Screen Recording permission not yet granted) before
	// committing to it as the monitor source. Capturer.Start is
	// idempotent, so the StreamCapturer below calling Start again when
	// the coordinator starts the session is a no-op -- real capture,
	// started here, just keeps running.
	if err := capturer.Start(); err != nil {
		fmt.Printf("meetingaudio: guest audio capture unavailable, starting mic-only (%v) — check Screen Recording permission in System Settings > Privacy & Security\n", err)
		return nil, nil
	}

	return audio.NewStreamCapturer(capturer, audio.DefaultWindowSize, 128), nil
}

// isTeamsPID reports whether pid (an audio-active process from
// audiosources.ListActive) belongs to the Teams app family, by app
// identity (name/bundle ID) rather than process ancestry — see the
// comment at its call site above for why ancestry alone isn't
// reliable for Teams' various helper/module processes.
func isTeamsPID(pid int32) bool {
	sources, err := audiosources.ListActive()
	if err != nil {
		return false
	}
	for _, s := range sources {
		if int32(s.PID) != pid {
			continue
		}
		return strings.Contains(strings.ToLower(s.Name), "teams") ||
			strings.Contains(strings.ToLower(s.BundleID), "teams")
	}
	return false
}
