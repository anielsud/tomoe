//go:build darwin

package backend

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework ApplicationServices -framework CoreGraphics
#import <AVFoundation/AVFoundation.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>

// Microphone: 0 = not asked yet, 1 = restricted, 2 = denied, 3 = granted
// (AVAuthorizationStatus).
static int perm_mic_status(void) {
	return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

// Shows the system prompt if the user hasn't been asked yet; a no-op
// afterwards.
static void perm_mic_request(void) {
	[AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {}];
}

static int perm_screen_status(void) {
	return CGPreflightScreenCaptureAccess() ? 1 : 0;
}

// Shows the system prompt the first time only.
static void perm_screen_request(void) {
	CGRequestScreenCaptureAccess();
}

static int perm_accessibility_status(void) {
	return AXIsProcessTrusted() ? 1 : 0;
}

// Shows the system prompt, which offers to open System Settings.
static void perm_accessibility_request(void) {
	NSDictionary *opts = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
	AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)opts);
}
*/
import "C"

import (
	"os/exec"
)

// macPermission is one macOS privacy permission Tomoe needs.
type macPermission struct {
	id, name, neededFor string
	// settingsPane is the System Settings > Privacy & Security pane.
	settingsPane string
	granted      func() bool
	// request shows the system prompt if one can still be shown. Returns
	// false when macOS won't prompt again (the user already answered), so
	// the only way to change it is System Settings.
	request func() bool
	// restartNote is shown after granting, when macOS only applies the
	// change to a relaunched app.
	restartNote string
}

var macPermissions = []macPermission{
	{
		id: "perm-microphone", name: "Microphone",
		neededFor:    "Recording your voice in meetings and dictation",
		settingsPane: "Privacy_Microphone",
		granted:      func() bool { return C.perm_mic_status() == 3 },
		request: func() bool {
			if C.perm_mic_status() != 0 {
				return false
			}
			C.perm_mic_request()
			return true
		},
	},
	{
		id: "perm-screen", name: "Screen Recording",
		neededFor:    "Capturing meeting audio from other apps, and reading speaker names from Teams",
		settingsPane: "Privacy_ScreenCapture",
		granted:      func() bool { return C.perm_screen_status() == 1 },
		request: func() bool {
			C.perm_screen_request()
			return false // only prompts once ever, so always open Settings too
		},
		restartNote: "Restart Tomoe after granting it",
	},
	{
		id: "perm-accessibility", name: "Accessibility",
		neededFor:    "Pasting dictated text into other apps",
		settingsPane: "Privacy_Accessibility",
		granted:      func() bool { return C.perm_accessibility_status() == 1 },
		request: func() bool {
			C.perm_accessibility_request()
			return true
		},
	},
}

// openPrivacySettings opens System Settings at a Privacy & Security pane.
func openPrivacySettings(pane string) error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?"+pane).Run()
}
