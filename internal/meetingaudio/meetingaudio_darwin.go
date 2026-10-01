//go:build darwin

package meetingaudio

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/audio"
	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
	"github.com/sosuke-ai/tomoe-pc/internal/guestaudio"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// NewMonitorSource captures system audio via ScreenCaptureKit, per
// sourceHint (from the frontend's source picker — see
// internal/audiosources.ListActive and internal/backend's
// ListAudioSources/StartSession):
//   - NoSource: no monitor capture, mic-only.
//   - AutoSource: the meeting app if one is making sound, else the whole
//     system (see NewAutoMonitorSource, which can also move to the app
//     later).
//   - "everything", or "" (the default): the whole system's audio
//     output, not tied to any one app
//     (internal/guestaudio.NewSystemCapturer). Speakers are separated
//     the same as for one app's audio.
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
	if sourceHint == NoSource {
		return nil, nil
	}
	if sourceHint == AutoSource || sourceHint == "" {
		stream, _, err := NewAutoMonitorSource()
		return stream, err
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
		if capturer, err = appCapturer(int32(pid)); err != nil {
			fmt.Printf("meetingaudio: no window found for selected app (PID %d), starting mic-only: %v\n", pid, err)
			return nil, nil
		}
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

// appCapturer captures one app's audio, via a window it owns.
func appCapturer(pid int32) (audio.Capturer, error) {
	var windowID teamsvideo.WindowID
	var err error
	if isTeamsPID(pid) {
		// CoreAudio's audio-active PID for Teams is frequently a
		// short-lived helper/module process (found live: "Microsoft
		// Teams WebView", and later a differently-named "modulehost"
		// helper for a different call) that isn't even a descendant
		// of the main Teams process via the normal parent-PID chain
		// FindWindowForPID walks -- some of these helpers are
		// launchd-spawned XPC services with PID 1 as their parent,
		// not a child of Teams itself. Once we know from the source's
		// own app identity (not process ancestry) that this is Teams,
		// skip PID/window matching entirely and go straight to the
		// existing, independently-proven title-based finder.
		windowID, err = teamsvideo.FindMeetingWindow()
	} else {
		windowID, err = teamsvideo.FindWindowForPID(pid)
	}
	if err != nil {
		return nil, err
	}
	return guestaudio.NewWindowCapturer(uint32(windowID)), nil
}

// meetingApps are the apps AutoSource captures, most likely first,
// matched against an audio source's name and bundle ID.
var meetingApps = []string{"teams", "zoom", "webex", "facetime", "discord", "slack"}

// activeMeetingApp is the meeting app producing audio right now, if any.
func activeMeetingApp() (audiosources.Source, bool) {
	sources, err := audiosources.ListActive()
	if err != nil {
		return audiosources.Source{}, false
	}
	for _, app := range meetingApps {
		for _, s := range sources {
			if strings.Contains(strings.ToLower(s.Name+" "+s.BundleID), app) {
				return s, true
			}
		}
	}
	return audiosources.Source{}, false
}

// Auto is an AutoSource capture: the meeting app if one is making sound,
// else the whole system until one does (see TryMeetingApp).
type Auto struct {
	sw *audio.SwitchCapturer

	mu      sync.Mutex
	current string
	onApp   bool
}

// Current names what's being captured ("Everything" or the app's name).
func (a *Auto) Current() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// TryMeetingApp switches from the whole system to the meeting app if one
// is making sound now, returning its name. Does nothing once on an app.
func (a *Auto) TryMeetingApp() (string, bool) {
	a.mu.Lock()
	onApp := a.onApp
	a.mu.Unlock()
	if onApp {
		return "", false
	}
	src, ok := activeMeetingApp()
	if !ok {
		return "", false
	}
	c, err := appCapturer(int32(src.PID))
	if err != nil {
		return "", false
	}
	if err := a.sw.Switch(c); err != nil {
		fmt.Printf("meetingaudio: couldn't switch to %s: %v\n", src.Name, err)
		return "", false
	}
	a.mu.Lock()
	a.current, a.onApp = src.Name, true
	a.mu.Unlock()
	return src.Name, true
}

// NewAutoMonitorSource starts AutoSource capture: the meeting app making
// sound now if there is one, else the whole system (call TryMeetingApp
// later to move to the app once it starts). Returns nil, nil if capture
// isn't available at all (mic-only, as NewMonitorSource).
func NewAutoMonitorSource() (*audio.StreamCapturer, *Auto, error) {
	a := &Auto{current: "Everything"}
	var first audio.Capturer
	if src, ok := activeMeetingApp(); ok {
		if c, err := appCapturer(int32(src.PID)); err == nil {
			first, a.current, a.onApp = c, src.Name, true
		}
	}
	if first == nil {
		first = guestaudio.NewSystemCapturer()
	}
	a.sw = audio.NewSwitchCapturer(first)
	if err := a.sw.Start(); err != nil {
		fmt.Printf("meetingaudio: guest audio capture unavailable, starting mic-only (%v) — check Screen Recording permission in System Settings > Privacy & Security\n", err)
		return nil, nil, nil
	}
	return audio.NewStreamCapturer(a.sw, audio.DefaultWindowSize, 128), a, nil
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
