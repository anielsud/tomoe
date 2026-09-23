package meeting

import "strings"

// knownBrowserBinaries maps browser application names (as reported by PulseAudio)
// to their xdotool WM class names for window title lookup.
var knownBrowserBinaries = map[string]bool{
	"Google Chrome":        true,
	"Google Chrome input":  true,
	"Chromium":             true,
	"Chromium input":       true,
	"Firefox":              true,
	"Firefox input":        true,
	"Brave Browser":        true,
	"Brave Browser input":  true,
	"Microsoft Edge":       true,
	"Microsoft Edge input": true,
}

// nativeAppPlatforms maps PulseAudio application names of native meeting
// apps to their platform. These can be identified without window title lookup.
var nativeAppPlatforms = map[string]Platform{
	"ZOOM VoiceEngine": PlatformZoom,
	"zoom":             PlatformZoom,
	"Slack":            PlatformSlack,
	"slack":            PlatformSlack,
}

// identifyPlatform determines the meeting platform from PulseAudio metadata
// and, for browser-based meetings, from the window title (looked up via
// getWindowTitleByPID, which is platform-specific).
func identifyPlatform(appName string, pid int) Platform {
	// Check native app names first (no window title needed)
	if p, ok := nativeAppPlatforms[appName]; ok {
		return p
	}

	// For browser-based apps, look up the window title
	if knownBrowserBinaries[appName] {
		title := getWindowTitleByPID(pid)
		if title != "" {
			return matchPlatformFromTitle(title)
		}
	}

	return PlatformUnknown
}

// matchPlatformFromTitle matches meeting platform keywords in a window title.
func matchPlatformFromTitle(title string) Platform {
	lower := strings.ToLower(title)

	switch {
	case strings.Contains(lower, "microsoft teams"):
		return PlatformTeams
	case strings.Contains(lower, "meet -") || strings.Contains(lower, "meet.google.com"):
		return PlatformMeet
	case strings.Contains(lower, "zoom"):
		return PlatformZoom
	case strings.Contains(lower, "webex"):
		return PlatformWebex
	case strings.Contains(lower, "slack"):
		return PlatformSlack
	}

	return PlatformUnknown
}
