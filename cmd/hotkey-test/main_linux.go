// hotkey-test: diagnostic tool to test global hotkey registration and X11 event capture.
package main

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
#include <X11/keysym.h>
#include <stdio.h>
#include <string.h>

#define NUMLOCK_MASK   Mod2Mask
#define CAPSLOCK_MASK  LockMask
#define SCROLLLOCK_MASK Mod3Mask

static Display *test_display = NULL;
static int grab_error_count = 0;

static int test_error_handler(Display *d, XErrorEvent *e) {
	char buf[256];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	fprintf(stderr, "  X11 error: request=%d error=%d (%s)\n",
		e->request_code, e->error_code, buf);
	if (e->request_code == 33) { // X_GrabKey
		grab_error_count++;
	}
	return 0;
}

static int test_init(const char *display_name) {
	test_display = XOpenDisplay(display_name);
	if (test_display == NULL) return -1;
	XSetErrorHandler(test_error_handler);
	return 0;
}

static const char* test_display_string() {
	if (test_display == NULL) return "(null)";
	return DisplayString(test_display);
}

static int test_grab_with_locks(unsigned int mod, unsigned int keycode) {
	if (test_display == NULL) return -1;
	Window root = DefaultRootWindow(test_display);
	grab_error_count = 0;
	unsigned int locks[] = {0, NUMLOCK_MASK, CAPSLOCK_MASK, SCROLLLOCK_MASK,
		NUMLOCK_MASK|CAPSLOCK_MASK, NUMLOCK_MASK|SCROLLLOCK_MASK,
		CAPSLOCK_MASK|SCROLLLOCK_MASK, NUMLOCK_MASK|CAPSLOCK_MASK|SCROLLLOCK_MASK};
	for (int i = 0; i < 8; i++) {
		XGrabKey(test_display, keycode, mod | locks[i], root,
			False, GrabModeAsync, GrabModeAsync);
	}
	XSync(test_display, False);
	return grab_error_count;
}

static void test_ungrab(unsigned int mod, unsigned int keycode) {
	if (test_display == NULL) return;
	Window root = DefaultRootWindow(test_display);
	unsigned int locks[] = {0, NUMLOCK_MASK, CAPSLOCK_MASK, SCROLLLOCK_MASK,
		NUMLOCK_MASK|CAPSLOCK_MASK, NUMLOCK_MASK|SCROLLLOCK_MASK,
		CAPSLOCK_MASK|SCROLLLOCK_MASK, NUMLOCK_MASK|CAPSLOCK_MASK|SCROLLLOCK_MASK};
	for (int i = 0; i < 8; i++) {
		XUngrabKey(test_display, keycode, mod | locks[i], root);
	}
	XSync(test_display, False);
}

static unsigned int test_keysym_to_keycode(unsigned int keysym) {
	if (test_display == NULL) return 0;
	return XKeysymToKeycode(test_display, keysym);
}

// Wait for next X event with a timeout using select().
// Returns event type or 0 on timeout, -1 on error.
static int test_wait_event(int timeout_ms) {
	if (test_display == NULL) return -1;
	int fd = ConnectionNumber(test_display);
	fd_set fds;
	struct timeval tv;
	// First drain any pending events
	while (XPending(test_display)) {
		XEvent ev;
		XNextEvent(test_display, &ev);
		return ev.type;
	}
	// Wait with timeout
	FD_ZERO(&fds);
	FD_SET(fd, &fds);
	tv.tv_sec = timeout_ms / 1000;
	tv.tv_usec = (timeout_ms % 1000) * 1000;
	int ret = select(fd + 1, &fds, NULL, NULL, &tv);
	if (ret > 0) {
		XEvent ev;
		XNextEvent(test_display, &ev);
		return ev.type;
	}
	return 0; // timeout
}

// Listen for raw key events on root window (no grab — just event selection).
static void test_select_root_keys() {
	if (test_display == NULL) return;
	Window root = DefaultRootWindow(test_display);
	XSelectInput(test_display, root, KeyPressMask | KeyReleaseMask);
	XSync(test_display, False);
}

static void test_close() {
	if (test_display != NULL) {
		XCloseDisplay(test_display);
		test_display = NULL;
	}
}
*/
import "C"

import (
	"fmt"
	"os"
	"time"
)

func main() {
	// Try to detect DISPLAY
	display := os.Getenv("DISPLAY")
	fmt.Printf("DISPLAY env: %q\n", display)

	var cDisplay *C.char
	if display != "" {
		cDisplay = C.CString(display)
	}

	if C.test_init(cDisplay) != 0 {
		fmt.Fprintf(os.Stderr, "FATAL: Cannot open X11 display %q\n", display)
		os.Exit(1)
	}
	defer C.test_close()

	fmt.Printf("X11 display opened: %s\n", C.GoString(C.test_display_string()))

	// Phase 1: Raw root window key events (no grab)
	fmt.Println("\n=== Phase 1: Raw root window event selection (no grabs) ===")
	fmt.Println("Selecting KeyPress events on root window...")
	C.test_select_root_keys()
	fmt.Println("Press ANY key within 5 seconds...")

	gotRawEvent := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evType := C.test_wait_event(500) // 500ms poll
		if evType > 0 {
			fmt.Printf("  Raw event received! type=%d (KeyPress=%d, KeyRelease=%d)\n",
				evType, C.KeyPress, C.KeyRelease)
			gotRawEvent = true
			break
		}
	}
	if !gotRawEvent {
		fmt.Println("  NO raw events received. Root window event selection may be blocked.")
		fmt.Println("  This typically means another window manager is consuming root events.")
	}

	// Phase 2: Test grabs with specific bindings
	fmt.Println("\n=== Phase 2: XGrabKey tests ===")

	type testCase struct {
		name   string
		mod    C.uint
		keysym C.uint
	}

	tests := []testCase{
		{"Ctrl+Alt+R", C.ControlMask | C.Mod1Mask, 0x0072},
		{"Ctrl+Shift+R", C.ControlMask | C.ShiftMask, 0x0072},
		{"Super+Shift+R", C.Mod4Mask | C.ShiftMask, 0x0072},
		{"Super+Shift+M", C.Mod4Mask | C.ShiftMask, 0x006d},
	}

	for _, tc := range tests {
		keycode := C.test_keysym_to_keycode(tc.keysym)
		fmt.Printf("\n--- %s (mod=0x%x keycode=%d keysym=0x%x) ---\n",
			tc.name, uint(tc.mod), uint(keycode), uint(tc.keysym))

		if keycode == 0 {
			fmt.Println("  SKIP: keysym could not be mapped to keycode")
			continue
		}

		errCount := C.test_grab_with_locks(tc.mod, keycode)
		if errCount > 0 {
			fmt.Printf("  GRAB FAILED: %d XGrabKey errors (likely BadAccess — another app has this grab)\n", errCount)
			C.test_ungrab(tc.mod, keycode)
			continue
		}
		fmt.Printf("  Grab OK (8 lock combos). Press %s within 5 seconds...\n", tc.name)

		gotGrab := false
		grabDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(grabDeadline) {
			evType := C.test_wait_event(500)
			if evType == C.KeyPress {
				fmt.Printf("  GOT KEYPRESS via grab!\n")
				gotGrab = true
				break
			} else if evType > 0 {
				fmt.Printf("  Got event type=%d (not KeyPress)\n", evType)
			}
		}
		if !gotGrab {
			fmt.Println("  No keypress received in 5s (timeout)")
		}

		C.test_ungrab(tc.mod, keycode)
	}

	fmt.Println("\n=== Done ===")
}
