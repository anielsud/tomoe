//go:build darwin

package meetingaudio

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/audiosources"
)

func TestBrowserOwner(t *testing.T) {
	for _, tc := range []struct{ name, bundle, want string }{
		{"Google Chrome Helper", "com.google.Chrome.helper", "Google Chrome"},
		{"Microsoft Edge Helper", "com.microsoft.edgemac.helper", "Microsoft Edge"},
		{"Brave Browser Helper", "com.brave.Browser.helper", "Brave Browser"},
		{"Safari Graphics and Media", "com.apple.WebKit.GPU", "Safari"},
		{"Firefox", "org.mozilla.firefox", "Firefox"},
		{"Music", "com.apple.Music", ""},
	} {
		if got := browserOwner(audiosources.Source{Name: tc.name, BundleID: tc.bundle}); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
