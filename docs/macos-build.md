# macOS build, packaging, and signing

Covers getting `cmd/tomoe` (CLI) and `cmd/tomoe-gui` (Wails GUI) to
build, run, and stay permission-granted across rebuilds on macOS. See
[`macos-support.md`](macos-support.md) for overall status/roadmap.

## CLI dictation (`cmd/tomoe`)

Package-by-package porting notes:

- `internal/hotkey/hotkey_darwin.go` — global hotkey via Carbon's
  `RegisterEventHotKey`/`InstallEventHandler` (no Accessibility
  permission needed for this part, unlike auto-type below). `"Super"`
  in config bindings maps to ⌘, so existing strings like
  `"Super+Shift+S"` work unchanged on both platforms.
- `internal/clipboard/clipboard_darwin.go`,
  `internal/notify/notify_darwin.go` — `osascript` (`keystroke` /
  `display notification`), the macOS equivalents of `xdotool type` /
  `notify-send`. `TypeText` needs Accessibility permission granted to
  the process; `Send` doesn't.
- `internal/audio`, `internal/sigfix`, and the tray-manager files in
  `internal/backend`/`internal/daemon` turned out to already be fully
  portable — `_linux.go`-suffixed but containing no actual
  Linux-specific code, so the fix was just dropping the suffix.
- `internal/meeting` gained a `detect_darwin.go` stub: no macOS
  equivalent of PulseAudio's dual-stream meeting-detection signal
  exists yet, so `pulseInit()` always returns an error there. Every
  call site already treats a failed `Detector.Start()` as "feature
  disabled, continue," so this is silent and safe.
- **Real bug: `fyne.io/systray`'s darwin backend must run its native
  loop on the actual OS main thread** (a hard Cocoa/AppKit
  requirement) — calling `systray.Run` from a goroutine crashes with a
  low-level AppKit assertion failure the first time the tray tries to
  draw. Fixed via `internal/daemon/run_darwin.go`: on darwin,
  `daemon.Run()`'s entire body runs inside `systray.Run`'s `onReady`
  callback instead of the other way around (Linux keeps the tray in a
  goroutine, unchanged).
- Verified live: `osascript`-simulated hotkey triggers real dictation
  end-to-end (mic capture → Parakeet TDT → clipboard/auto-paste),
  clean start/stop, clean `SIGTERM` shutdown.
- `.github/workflows/ci.yml`'s `macos-14` job runs `go vet ./...`
  unscoped — an earlier `internal/teamsvideo` advisory (`possible
  misuse of unsafe.Pointer`) that once required scoping this down was
  fixed with a small `cfarray_is_null` C helper doing the `NULL` check
  instead of a Go-side `unsafe.Pointer` comparison.

## GUI (`cmd/tomoe-gui`)

Two separate problems:

1. **Linker gap:** newer Xcode SDKs need `-framework
   UniformTypeIdentifiers` for Wails' own darwin frontend package to
   link (otherwise `Undefined symbols ... _OBJC_CLASS_$_UTType`).
   `make build`/`make build-gui` pass this via `CGO_LDFLAGS` on darwin
   (see the `UNAME` branch in the Makefile).
2. **Window + tray coexistence.** Wails' own window already owns the
   real Cocoa main thread and calls `[NSApp run]` itself, so systray
   can't claim it the way the CLI daemon does (which has no competing
   window). Fixed using `fyne.io/systray`'s `RunWithExternalLoop`
   (built for exactly this — an app that already owns the native run
   loop): its `registerSystray` C implementation checks an
   internal-loop flag and, in external-loop mode, never calls `[NSApp
   run]` and never replaces `NSApplication`'s delegate, so it can't
   clobber Wails' own delegate. The `start`/`end` functions it hands
   back still do direct AppKit calls (building/tearing down the
   `NSStatusItem`) and so still need the real main thread — dispatched
   there via `dispatch_async` to GCD's main queue
   (`internal/backend/dispatch_darwin.go`), which works regardless of
   which goroutine calls it and regardless of whether Wails' `[NSApp
   run]` has started pumping yet. Ordinary menu interaction (`onReady`,
   item clicks) needed no such treatment — systray's ObjC side already
   dispatches those via `performSelectorOnMainThread` internally. See
   `internal/backend/tray_start_darwin.go`.

   Verified live: window and tray status item both present
   simultaneously, global hotkey starts/stops dictation through the
   GUI, clean shutdown. One AppleScript-level nit found and *not*
   chased: the tray's dropdown menu isn't introspectable via System
   Events (`exists menu 1 of menu bar item ...` returns false even
   after a successful click) — looks like a pre-existing quirk of how
   `fyne.io/systray` presents its macOS menu, unrelated to
   `RunWithExternalLoop`; the menu works fine when actually clicked.

## Code signing and TCC permission persistence

**Ad-hoc signing (`codesign --sign -`) has no consistent identity
across builds, and macOS ties Screen Recording/Microphone/Accessibility
grants to the app's signing identity.** Found live: System Settings
kept showing "Tomoe" as enabled for Screen Recording, yet a freshly
rebuilt, freshly reinstalled copy still couldn't see meeting windows or
their titles, because the identity backing that grant no longer
matched the current binary.

A separate red herring surfaced during the same debugging session:
running the binary directly from a terminal (rather than via
Dock/Launchpad) appeared to work — but only because the terminal's own
already-granted Screen Recording permission covers processes it
launches directly. That says nothing about whether the *installed*
`.app` has a working grant of its own.

Fixed with `make dev-cert-mac`: creates a stable, self-signed local
code-signing identity ("Tomoe Dev Signing") once in the login keychain
(`openssl req -x509 ... -addext extendedKeyUsage=codeSigning` +
`security import`). `make install-gui-mac` signs every build with that
same identity (`codesign --sign "Tomoe Dev Signing"`) instead of
ad-hoc. **Verified across many subsequent rebuilds within the same
development session:** TCC grants (Screen Recording, Microphone) have
survived every `make install-gui-mac` rebuild since the identity was
created, with no re-prompt.

**Porting lesson:** if a macOS app's permission grants seem to
"randomly" disappear between builds, check the signing identity first
— ad-hoc signing is the most likely cause, and a terminal's own
inherited grant can mask the problem during quick manual testing
(binary run directly) right up until the actual packaged `.app` is
tested.
