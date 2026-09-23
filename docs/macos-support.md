# macOS Support (in progress)

Status: **both `cmd/tomoe` (CLI dictation) and `cmd/tomoe-gui` build,
run, and have been verified end-to-end on real macOS hardware** —
global hotkey (Carbon) → mic capture → Parakeet TDT transcription →
clipboard/auto-type all work in both, and the GUI's window and tray
icon now coexist without crashing. Meeting-mode's macOS-specific pieces
(teamsvideo OCR, guestaudio wiring, real meeting detection,
speaker-cluster labeling) are still not integrated — see
[Status](#status) for exactly what's left.

This isn't a straight port of the Linux path. Linux's speaker labeling
(`internal/speaker`) works from audio alone: cluster the monitor-source
embeddings, label clusters "Person N". macOS adds a second, independent
naming signal that Linux has no equivalent of — Teams renders a visible
ring around whichever tile is speaking, with a plain-text name label under
it. That's a real name, not an audio cluster ID, and it's available before
any speech is even transcribed. The design below exists to combine the
two rather than pick one.

## Why not just port the audio path as-is

Every other cross-platform package (`hotkey`, `clipboard`, `notify`) is a
1:1 swap: same interface, new backend. Speaker naming isn't, because the
input signal is structurally different per platform:

| | Linux | macOS |
|---|---|---|
| Meeting detection | PulseAudio: simultaneous source-output + sink-input from one PID | not yet built (no macOS equivalent implemented) |
| Speaker naming signal | none — audio embeddings only | Teams' active-speaker ring + name label (visual, OCR'd) |
| Speaker naming today | `internal/speaker` clusters embeddings → "Person N" | same clustering, but a cluster can be *labeled* by a video hint instead of staying anonymous |

The plan is not "replace clustering with the video signal." It's: keep
`internal/speaker`'s embedding + clustering pipeline exactly as it runs on
Linux today — unchanged — and add a second input that assigns a real name
to a cluster ID whenever a confident visual hint lands, carrying that name
forward for that cluster's later turns even without a fresh hint. A
cluster that never gets a hint (no video signal, or a dial-in participant
with no tile) still gets labeled "Person N", exactly like Linux does now.
This also gives persistent per-person voiceprints a natural home later: a
cluster's embedding centroid *is* the voiceprint, keyed to whatever name a
hint resolved it to.

## Architecture

```
                    Teams window (CGWindowID)
                              │
              ┌───────────────┴────────────────┐
              │                                  │
      internal/teamsvideo              internal/guestaudio
      (CoreGraphics capture,           (ScreenCaptureKit tap,
       ring detection, OCR)             one window's audio)
              │                                  │
        naming hint                        raw audio (planar
        {name, ts}                         stereo → mono, cgo)
              │                                  │
              │                          internal/speaker
              │                    (embed + cluster — UNCHANGED
              │                     from the Linux path)
              │                                  │
              │                          cluster ID + embedding
              │                                  │
              └──────────────┬───────────────────┘
                              ▼
                    label cluster ID with name
                    if a fresh hint is available,
                    else carry forward / "Person N"
                              │
                              ▼
                    Segment{speaker, text, ...}
                    (same shape internal/live already emits)
```

`internal/teamsvideo` and `internal/guestaudio` are independent of each
other and of `internal/speaker` — neither knows the other exists. The
labeling step above is the only new logic; it hasn't been built yet (see
Status).

## `internal/teamsvideo`

Window discovery (`FindMeetingWindow`) and frame capture
(`CaptureWindowRGB`) via CoreGraphics (`CGWindowListCopyWindowInfo`,
`CGWindowListCreateImage`). Pure C API — no Objective-C shim needed for
this part. Validated live: captures real, correctly-colored frames from
the active Teams window regardless of on-screen state.

**Known risk:** `CGWindowListCreateImage` is marked `obsoleted` (not just
deprecated) as of the macOS 15 SDK — a hard compile error unless the cgo
build pins `-mmacosx-version-min=14.0`. It still works at runtime on
current macOS, but Apple could remove the symbol outright in a future
release. The durable fix is porting frame capture to ScreenCaptureKit
(same framework `internal/guestaudio` already uses for audio), not
something to defer indefinitely.

**Not yet ported:** ring detection (color/shape connected-components —
pure Go, no cgo needed, the most self-contained piece left) and name-label
OCR (needs its own Objective-C shim, this time for `Vision.framework`).

## `internal/guestaudio`

`SCStream`-based audio tap scoped to one window via `SCContentFilter`, so
it captures only that window's audio (e.g. only the Teams call, not the
whole desktop). Objective-C shim (`bridge_darwin.m`) implements the
`SCStreamOutput` delegate; Go only ever sees raw sample buffers via a
`//export`'d callback (`callback_darwin.go`).

Validated live: 401 callbacks / ~385,000 real samples over 8 seconds at
48kHz against an idle (non-speaking) Teams window — consistent, repeatable
across multiple runs.

**Known quirk:** ScreenCaptureKit delivers **planar** stereo float32 (all
of channel 0, then all of channel 1), not interleaved. The downmix
averages the two channel halves — get this wrong and audio decodes to
fluent-sounding transcription that's semantically nonsense, not a crash,
which makes it easy to miss. Handled correctly in `callback_darwin.go`
from the start.

**Known reliability gap:** one segfault was observed on a cold first run,
before any of the tap's own checkpoints had printed — looked like a
one-time initialization race (possibly the process's first-ever
window-server/XPC handshake). Not reproduced across several subsequent
runs. Worth monitoring, not yet root-caused.

## Status

### Phase 1 — CLI dictation (`tomoe`): done

`cmd/tomoe` builds and runs on macOS. What it took, package by package:

- `internal/hotkey/hotkey_darwin.go` — global hotkey via Carbon's
  `RegisterEventHotKey`/`InstallEventHandler` (no Accessibility
  permission needed for this part, unlike auto-type below). "Super" in
  config bindings maps to ⌘ so existing binding strings like
  `"Super+Shift+S"` work unchanged on both platforms.
- `internal/clipboard/clipboard_darwin.go`, `internal/notify/notify_darwin.go`
  — `osascript` (`keystroke` / `display notification`), the macOS
  equivalents of `xdotool type` / `notify-send`. `TypeText` needs
  Accessibility permission granted to the process; `Send` doesn't.
- `internal/audio`, `internal/sigfix`, and the tray-manager files in
  `internal/backend`/`internal/daemon` turned out to already be fully
  portable — they were `_linux.go`-suffixed but contained no actual
  Linux-specific code, so the fix was just dropping the suffix (see
  `capture_malgo.go`, `fix.go`, `tray.go`).
- `internal/meeting` gained a `detect_darwin.go` stub: automatic meeting
  detection has no macOS implementation yet (no equivalent of
  PulseAudio's dual-stream signal — that's still phase 2), so
  `pulseInit()` always returns an error there. Every call site already
  treats a failed `Detector.Start()` as "feature disabled, continue," so
  this is silent and safe, not a crash.
- **The one real bug this surfaced:** `fyne.io/systray`'s darwin backend
  must run its native loop on the actual OS main thread (a hard Cocoa/
  AppKit requirement) — calling `systray.Run` from a goroutine, as the
  daemon originally did unconditionally, crashes with a low-level AppKit
  assertion failure the first time the tray tries to draw. Fixed via
  `internal/daemon/run_linux.go` / `run_darwin.go`: on darwin,
  `daemon.Run()`'s entire body (hotkey registration, the event select
  loop, etc.) now runs inside `systray.Run`'s `onReady` callback instead
  of the other way around; Linux's behavior (tray in a goroutine,
  `Run()`'s body proceeds immediately) is unchanged.
- Verified live on real hardware: `osascript`-simulated ⌘⇧S actually
  triggers the registered Carbon hot key, starts mic capture, transcribes
  real speech via Parakeet TDT, and a second press stops it cleanly;
  SIGTERM shuts the daemon down cleanly too.
- `.github/workflows/ci.yml` gained a `macos-14` job building/testing
  `cmd/tomoe`'s surface (see that job's comment for why its `go vet` is
  scoped rather than `./...` — it excludes the still-unintegrated
  `internal/teamsvideo`, which has a pre-existing `go vet` advisory
  unrelated to this phase).

**`cmd/tomoe-gui` on macOS — also fixed.** Two separate problems, both
resolved:

1. **Linker gap:** newer Xcode SDKs need `-framework
   UniformTypeIdentifiers` for Wails' own darwin frontend package to
   link (otherwise `Undefined symbols ... _OBJC_CLASS_$_UTType`).
   `make build`/`make build-gui` now pass this via `CGO_LDFLAGS` on
   darwin (see the `UNAME` branch in the Makefile).
2. **Window + tray coexistence:** Wails' own window already owns the
   real Cocoa main thread and calls `[NSApp run]` itself, so systray
   can't claim it the way `run_darwin.go` claims it for the CLI daemon
   (which has no competing window). Fixed using
   `fyne.io/systray`'s `RunWithExternalLoop` (built for exactly this —
   an app that already owns the native run loop): its
   `registerSystray` C implementation checks an internal-loop flag and,
   in external-loop mode, never calls `[NSApp run]` and never replaces
   `NSApplication`'s delegate, so it can't clobber Wails' own delegate.
   The `start`/`end` functions it hands back still do direct AppKit
   calls (building/tearing down the `NSStatusItem`) and so still need
   the real main thread — dispatched there via `dispatch_async` to
   GCD's main queue (`internal/backend/dispatch_darwin.go`), which
   works regardless of which goroutine calls it and regardless of
   whether Wails' `[NSApp run]` has started pumping yet. Ordinary menu
   interaction (`onReady`, item clicks) needed no such treatment —
   systray's ObjC side already dispatches those via
   `performSelectorOnMainThread` internally, which is how the original
   `Run()`-based CLI path got away without this problem too.
   See `internal/backend/tray_start_darwin.go`.

Verified live: window and tray status item both present simultaneously
(confirmed via System Events, not just "process didn't crash"), the
global hotkey starts/stops real dictation through the GUI exactly like
the CLI, and shutdown (`SIGTERM` → `App.Shutdown` → `StopTray`) is
clean. One AppleScript-level nit found and *not* chased: the tray's
dropdown menu isn't introspectable via System Events (`exists menu 1 of
menu bar item ...` returns false even after a successful click) —
looks like a pre-existing quirk of how `fyne.io/systray` presents its
macOS menu (likely a manual popover rather than assigning
`NSStatusItem.menu` directly), unrelated to the `RunWithExternalLoop`
change; the menu itself works fine when actually clicked by a human.

### Phase 2 — meeting mode: not started

- `internal/teamsvideo` — window discovery and frame capture work and
  are validated live; ring detection and Vision.framework OCR (the
  name-label read) are not yet ported. Has a pre-existing `go vet`
  advisory (`possible misuse of unsafe.Pointer` in `window_darwin.go`)
  left as-is rather than patched blind — worth a proper look when this
  package is next touched.
- `internal/guestaudio` — works standalone, not wired into
  `internal/live`'s pipeline.
- Real meeting-start detection (the macOS equivalent of PulseAudio's
  dual-stream signal) — no design yet.
- The cluster-labeling step (video hint → speaker cluster ID) — not
  written.

## Background

The video-ring/OCR mechanism, the ScreenCaptureKit gotchas, and the full
evidentiary trail (why AXUIElement was tried and disproven first, how the
planar-audio bug was actually diagnosed, live test numbers against real
calls) live in a separate internal research repo,
`anielsud/tomoe-darwin` — a Python/pyobjc spike, not something this
package depends on or ports code from directly. Treat it as prior art for
*why* these mechanisms work, not as a second implementation to keep in
sync with this one.
