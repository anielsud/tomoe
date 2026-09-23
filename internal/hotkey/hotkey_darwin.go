//go:build darwin

package hotkey

/*
#cgo LDFLAGS: -framework Carbon -framework CoreFoundation
#include <Carbon/Carbon.h>

// Forward declaration of the Go-exported dispatch callback defined below.
extern void goHotKeyDispatch(UInt32 hkId);

static EventHandlerUPP hk_handler_upp = NULL;

static OSStatus hk_handler(EventHandlerCallRef nextHandler, EventRef event, void *userData) {
	EventHotKeyID hkID;
	OSStatus err = GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID,
		NULL, sizeof(hkID), NULL, &hkID);
	if (err == noErr) {
		goHotKeyDispatch(hkID.id);
	}
	return noErr;
}

// hk_install installs the single, process-wide hot key event handler.
// Must be called exactly once, before hk_pump, from the same thread.
static void hk_install(void) {
	EventTypeSpec spec = {kEventClassKeyboard, kEventHotKeyPressed};
	hk_handler_upp = NewEventHandlerUPP(hk_handler);
	InstallEventHandler(GetApplicationEventTarget(), hk_handler_upp, 1, &spec, NULL, NULL);
}

// hk_pump blocks forever pumping the calling thread's run loop, which is
// what actually delivers hot key events to the handler installed above.
// The darwin equivalent of X11's single dispatch loop shared across all
// listeners -- started once, on a dedicated OS-thread-locked goroutine.
static void hk_pump(void) {
	CFRunLoopRun();
}

static EventHotKeyRef hk_register(UInt32 keycode, UInt32 modifiers, UInt32 hkId) {
	EventHotKeyID id;
	id.signature = 'tmoe';
	id.id = hkId;
	EventHotKeyRef ref = NULL;
	OSStatus err = RegisterEventHotKey(keycode, modifiers, id, GetApplicationEventTarget(), 0, &ref);
	if (err != noErr) {
		return NULL;
	}
	return ref;
}

static void hk_unregister(EventHotKeyRef ref) {
	if (ref != NULL) {
		UnregisterEventHotKey(ref);
	}
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

// Global listener registry and single dispatch handler/run loop. Carbon
// hot key registration needs no elevated permission (no Accessibility
// grant required, unlike TypeText's `osascript keystroke`), so unlike
// X11's grab table there is no re-grab step after audio device init --
// see ReGrabAll below.
var (
	registryMu    sync.Mutex
	registry      = make(map[uint32]*darwinListener)
	nextID        uint32
	dispatchOnce  sync.Once
	dispatchReady = make(chan struct{})
)

// darwinListener implements Listener using Carbon's RegisterEventHotKey.
type darwinListener struct {
	id        uint32
	modifiers C.UInt32
	keycode   C.UInt32
	keydown   chan struct{}

	mu      sync.Mutex
	running bool
	ref     C.EventHotKeyRef
}

// NewListener creates a Listener for the given binding string.
func NewListener(bindingStr string) (Listener, error) {
	binding, err := ParseBinding(bindingStr)
	if err != nil {
		return nil, err
	}

	modifiers, err := carbonModifierFlags(binding.Modifiers)
	if err != nil {
		return nil, err
	}

	keycode, err := virtualKeyCode(binding.Key)
	if err != nil {
		return nil, err
	}

	return &darwinListener{
		id:        atomic.AddUint32(&nextID, 1),
		modifiers: modifiers,
		keycode:   keycode,
		keydown:   make(chan struct{}, 1),
	}, nil
}

func (l *darwinListener) Register() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return fmt.Errorf("hotkey already registered")
	}

	dispatchOnce.Do(func() {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			C.hk_install()
			close(dispatchReady)
			C.hk_pump() // blocks forever
		}()
	})
	<-dispatchReady

	ref := C.hk_register(l.keycode, l.modifiers, C.UInt32(l.id))
	if ref == nil {
		return fmt.Errorf("RegisterEventHotKey failed (binding may already be in use)")
	}
	l.ref = ref
	l.running = true

	registryMu.Lock()
	registry[l.id] = l
	registryMu.Unlock()

	fmt.Printf("hotkey: registered id=%d keycode=%d mod=0x%x\n", l.id, uint32(l.keycode), uint32(l.modifiers))
	return nil
}

func (l *darwinListener) Keydown() <-chan struct{} {
	return l.keydown
}

func (l *darwinListener) Unregister() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.running {
		return nil
	}
	l.running = false

	registryMu.Lock()
	delete(registry, l.id)
	registryMu.Unlock()

	C.hk_unregister(l.ref)
	l.ref = nil
	return nil
}

// ReGrabAll is a no-op on macOS. Carbon hot key registrations, unlike
// X11 key grabs, aren't disturbed by CoreAudio device init/teardown --
// kept only so the shared call sites in internal/backend and
// internal/daemon compile unchanged across platforms.
func ReGrabAll() {}

//export goHotKeyDispatch
func goHotKeyDispatch(hkId C.UInt32) {
	id := uint32(hkId)
	registryMu.Lock()
	l, ok := registry[id]
	registryMu.Unlock()

	if !ok {
		fmt.Printf("hotkey: unmatched event id=%d\n", id)
		return
	}
	select {
	case l.keydown <- struct{}{}:
		fmt.Printf("hotkey: dispatched id=%d\n", id)
	default:
		fmt.Printf("hotkey: dropped id=%d (channel full)\n", id)
	}
}

// carbonModifierFlags maps parsed Binding modifiers to Carbon event
// modifier flags. "Super" maps to cmdKey (⌘) so existing config strings
// like "Super+Shift+S" work unchanged on both platforms.
func carbonModifierFlags(mods []string) (C.UInt32, error) {
	var flags C.UInt32
	for _, m := range mods {
		switch m {
		case "Super":
			flags |= C.cmdKey
		case "Ctrl":
			flags |= C.controlKey
		case "Shift":
			flags |= C.shiftKey
		case "Alt":
			flags |= C.optionKey
		default:
			return 0, fmt.Errorf("unsupported modifier: %s", m)
		}
	}
	return flags, nil
}

// virtualKeyCode maps a key name to a macOS virtual keycode (kVK_*).
func virtualKeyCode(key string) (C.UInt32, error) {
	switch key {
	case "A":
		return C.kVK_ANSI_A, nil
	case "B":
		return C.kVK_ANSI_B, nil
	case "C":
		return C.kVK_ANSI_C, nil
	case "D":
		return C.kVK_ANSI_D, nil
	case "E":
		return C.kVK_ANSI_E, nil
	case "F":
		return C.kVK_ANSI_F, nil
	case "G":
		return C.kVK_ANSI_G, nil
	case "H":
		return C.kVK_ANSI_H, nil
	case "I":
		return C.kVK_ANSI_I, nil
	case "J":
		return C.kVK_ANSI_J, nil
	case "K":
		return C.kVK_ANSI_K, nil
	case "L":
		return C.kVK_ANSI_L, nil
	case "M":
		return C.kVK_ANSI_M, nil
	case "N":
		return C.kVK_ANSI_N, nil
	case "O":
		return C.kVK_ANSI_O, nil
	case "P":
		return C.kVK_ANSI_P, nil
	case "Q":
		return C.kVK_ANSI_Q, nil
	case "R":
		return C.kVK_ANSI_R, nil
	case "S":
		return C.kVK_ANSI_S, nil
	case "T":
		return C.kVK_ANSI_T, nil
	case "U":
		return C.kVK_ANSI_U, nil
	case "V":
		return C.kVK_ANSI_V, nil
	case "W":
		return C.kVK_ANSI_W, nil
	case "X":
		return C.kVK_ANSI_X, nil
	case "Y":
		return C.kVK_ANSI_Y, nil
	case "Z":
		return C.kVK_ANSI_Z, nil
	case "0":
		return C.kVK_ANSI_0, nil
	case "1":
		return C.kVK_ANSI_1, nil
	case "2":
		return C.kVK_ANSI_2, nil
	case "3":
		return C.kVK_ANSI_3, nil
	case "4":
		return C.kVK_ANSI_4, nil
	case "5":
		return C.kVK_ANSI_5, nil
	case "6":
		return C.kVK_ANSI_6, nil
	case "7":
		return C.kVK_ANSI_7, nil
	case "8":
		return C.kVK_ANSI_8, nil
	case "9":
		return C.kVK_ANSI_9, nil
	case "SPACE":
		return C.kVK_Space, nil
	case "RETURN", "ENTER":
		return C.kVK_Return, nil
	case "ESCAPE", "ESC":
		return C.kVK_Escape, nil
	case "TAB":
		return C.kVK_Tab, nil
	case "DELETE":
		return C.kVK_ForwardDelete, nil
	case "LEFT":
		return C.kVK_LeftArrow, nil
	case "RIGHT":
		return C.kVK_RightArrow, nil
	case "UP":
		return C.kVK_UpArrow, nil
	case "DOWN":
		return C.kVK_DownArrow, nil
	case "F1":
		return C.kVK_F1, nil
	case "F2":
		return C.kVK_F2, nil
	case "F3":
		return C.kVK_F3, nil
	case "F4":
		return C.kVK_F4, nil
	case "F5":
		return C.kVK_F5, nil
	case "F6":
		return C.kVK_F6, nil
	case "F7":
		return C.kVK_F7, nil
	case "F8":
		return C.kVK_F8, nil
	case "F9":
		return C.kVK_F9, nil
	case "F10":
		return C.kVK_F10, nil
	case "F11":
		return C.kVK_F11, nil
	case "F12":
		return C.kVK_F12, nil
	case "F13":
		return C.kVK_F13, nil
	case "F14":
		return C.kVK_F14, nil
	case "F15":
		return C.kVK_F15, nil
	case "F16":
		return C.kVK_F16, nil
	case "F17":
		return C.kVK_F17, nil
	case "F18":
		return C.kVK_F18, nil
	case "F19":
		return C.kVK_F19, nil
	case "F20":
		return C.kVK_F20, nil
	}
	return 0, fmt.Errorf("unsupported key: %s", key)
}
