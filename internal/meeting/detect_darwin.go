//go:build darwin

package meeting

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

// macOS detection uses the same signal as Linux — one app with a
// microphone stream and a playback stream at once — read from CoreAudio's
// per-process "is running input/output" flags (internal/audiosources)
// instead of PulseAudio's stream list. CoreAudio has no subscription for
// this that fits detect.go's event model, so the event loop polls once a
// second and turns changes into the same facility/event pairs PulseAudio
// would deliver. Everything above these helpers (debounce, start/stop
// events, the health check) is the shared code in detect.go.
//
// Streams are attributed to the app that owns them (a browser's audio
// helper process counts as the browser) and Tomoe's own mic stream is
// ignored, or every recording would look like a meeting.

// pollInterval is how often CoreAudio is asked which apps have audio
// running. A meeting is only confirmed after debounceDelay anyway.
const pollInterval = time.Second

// appNamesByBundle gives known meeting apps and browsers the names
// platform.go recognizes (the PulseAudio application names on Linux).
// Anything else keeps its own name and ends up PlatformUnknown.
var appNamesByBundle = map[string]string{
	"us.zoom.xos":                "zoom",
	"com.tinyspeck.slackmacgap":  "Slack",
	"com.microsoft.teams2":       "Microsoft Teams",
	"com.microsoft.teams":        "Microsoft Teams",
	"Cisco-Systems.Spark":        "Webex",
	"com.cisco.webexmeetingsapp": "Webex",
	"com.google.Chrome":          "Google Chrome",
	"org.chromium.Chromium":      "Chromium",
	"org.mozilla.firefox":        "Firefox",
	"com.brave.Browser":          "Brave Browser",
	"com.microsoft.edgemac":      "Microsoft Edge",
	"com.apple.Safari":           "Safari",
	"company.thebrowser.Browser": "Arc",
}

func init() {
	// Native Teams and Webex, and Safari and Arc as browsers, have no
	// Linux counterpart in platform.go's tables; adding them here keeps
	// Linux's identification exactly as it was.
	nativeAppPlatforms["Microsoft Teams"] = PlatformTeams
	nativeAppPlatforms["Webex"] = PlatformWebex
	knownBrowserBinaries["Safari"] = true
	knownBrowserBinaries["Arc"] = true
}

var (
	activeMu       sync.Mutex
	activeDetector *Detector
)

func pulseInit() error {
	if _, err := audiosources.ListStreams(true); err != nil {
		return fmt.Errorf("CoreAudio process list unavailable: %w", err)
	}
	return nil
}

func pulseSubscribe() error { return nil }

func pulseCleanup() {}

func pulseQuit() {}

func setActiveDetector(d *Detector) {
	activeMu.Lock()
	activeDetector = d
	activeMu.Unlock()
}

// streamChange is one app starting or stopping a microphone or playback
// stream, in the PulseAudio event vocabulary detect.go consumes.
type streamChange struct {
	facility, eventType int
}

// diffStreams is the events for the streams that appeared in or vanished
// from cur since prev (sets of app PIDs).
func diffStreams(prev, cur map[int]bool, facility int) []streamChange {
	var out []streamChange
	for pid := range cur {
		if !prev[pid] {
			out = append(out, streamChange{facility, paEventNew})
		}
	}
	for pid := range prev {
		if !cur[pid] {
			out = append(out, streamChange{facility, paEventRemove})
		}
	}
	return out
}

// pulseEventLoop polls until ctx ends, reporting each change to the
// active detector. Streams already running when it starts are the
// baseline, not events, as with PulseAudio's subscription.
func pulseEventLoop(ctx context.Context) {
	prevIn := pidSet(pulseListSourceOutputs())
	prevOut := pidSet(pulseListSinkInputs())
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		curIn := pidSet(pulseListSourceOutputs())
		curOut := pidSet(pulseListSinkInputs())
		changes := diffStreams(prevIn, curIn, paFacilitySourceOutput)
		changes = append(changes, diffStreams(prevOut, curOut, paFacilitySinkInput)...)
		prevIn, prevOut = curIn, curOut

		activeMu.Lock()
		d := activeDetector
		activeMu.Unlock()
		if d == nil {
			continue
		}
		for _, c := range changes {
			d.onSubscribeEvent(c.facility, c.eventType, 0)
		}
	}
}

func pidSet(streams []streamInfo) map[int]bool {
	m := make(map[int]bool, len(streams))
	for _, s := range streams {
		m[s.PID] = true
	}
	return m
}

func pulseListSinkInputs() []streamInfo { return listStreams(false) }

func pulseListSourceOutputs() []streamInfo { return listStreams(true) }

func listStreams(input bool) []streamInfo {
	sources, err := audiosources.ListStreams(input)
	if err != nil {
		return nil
	}
	return streamInfos(sources, os.Getpid())
}

// streamInfos converts apps to detector streams under the names
// platform.go knows, leaving out the app with pid self (Tomoe).
func streamInfos(sources []audiosources.Source, self int) []streamInfo {
	var out []streamInfo
	for _, s := range sources {
		if s.PID <= 0 || s.PID == self {
			continue
		}
		name := s.Name
		if known, ok := appNamesByBundle[s.BundleID]; ok {
			name = known
		}
		out = append(out, streamInfo{AppName: name, PID: s.PID})
	}
	return out
}

// getWindowTitleByPID is the title of one of the app's windows that
// names a meeting platform ("Meet - abc-defg-hij" in a browser), or "".
// Other apps' window titles need the Screen Recording permission Tomoe
// already asks for; without it every title is empty and the platform
// stays unknown.
func getWindowTitleByPID(pid int) string {
	windows, err := teamsvideo.ListWindows()
	if err != nil {
		return ""
	}
	var titles []string
	for _, w := range windows {
		if w.OwnerPID == pid {
			titles = append(titles, w.Title)
		}
	}
	return meetingTitle(titles)
}

// meetingTitle is the first title that names a meeting platform.
func meetingTitle(titles []string) string {
	for _, t := range titles {
		if t != "" && matchPlatformFromTitle(t) != PlatformUnknown {
			return t
		}
	}
	return ""
}

// processExists checks if a process with the given PID is still
// running, via a signal-0 liveness probe (the macOS equivalent of
// Linux's /proc/<pid> check, which has no macOS counterpart).
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// The PulseAudio event vocabulary detect.go consumes (and detect_test.go
// exercises), with the same shape as pulse_linux.go's real constants.
const (
	paFacilitySinkInput    = 0
	paFacilitySourceOutput = 1
	paEventNew             = 0
	paEventRemove          = 1
)

func isSourceOutputNew(facility, eventType int) bool {
	return facility == paFacilitySourceOutput && eventType == paEventNew
}

func isSourceOutputRemove(facility, eventType int) bool {
	return facility == paFacilitySourceOutput && eventType == paEventRemove
}

func isSinkInputNew(facility, eventType int) bool {
	return facility == paFacilitySinkInput && eventType == paEventNew
}

func isSinkInputRemove(facility, eventType int) bool {
	return facility == paFacilitySinkInput && eventType == paEventRemove
}
