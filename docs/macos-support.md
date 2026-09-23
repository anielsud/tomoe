# macOS Support (in progress)

Status: **`cmd/tomoe` (CLI dictation), `cmd/tomoe-gui`, and meeting
mode's dual-source audio capture all build, run, and have been verified
end-to-end on real macOS hardware.** Global hotkey (Carbon) → mic
capture → Parakeet TDT transcription → clipboard/auto-type all work;
the GUI's window and tray icon coexist without crashing; meeting mode
captures both the mic and the active Teams window's guest audio via
ScreenCaptureKit, same shape as Linux's mic+PulseAudio-monitor capture.
Speaker-cluster labeling stays "Person N" on both platforms — the
video-hint naming, real meeting detection, and everything past raw
audio capture is still not built. See [Status](#status) for exactly
what's left, and what it'll take.

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

**Two real bugs found and fixed integrating this into meeting mode**
(see Phase 2 below for the integration itself) — both root-caused via
`lldb` giving a real symbolicated backtrace, after Go's own crash
printer (which only shows Go-visible frames) pointed at the right cgo
call but not the actual fault:

1. **`dispatch_once` self-deadlock.** `guestaudio_ensure_app_context()`
   originally always hopped `[NSApplication sharedApplication]` over to
   the main queue via `dispatch_sync`, on the theory that callers might
   not already be on the main thread (true for `cmd/tomoe`'s daemon,
   which calls in from a goroutine spawned off systray's `onReady` — see
   `internal/daemon/run_darwin.go`). But `cmd/guest-audio-test`'s
   `main()` runs directly on the real main thread, and `dispatch_sync`
   to the queue you're already on is a same-thread deadlock —
   `libdispatch` detects this and deliberately traps rather than
   hanging (`lldb`'s backtrace: `guestaudio_start_tap` →
   `dispatch_once_callout` → `dispatch_sync_f_slow` →
   `DISPATCH_WAIT_FOR_QUEUE`). Fixed by checking `[NSThread
   isMainThread]` first and only hopping when actually needed.
2. **A real window-server/XPC handshake race.** `[NSApplication
   sharedApplication]` kicks off this process's first window-server
   connection but doesn't block until it's ready. The very next thing
   `guestaudio_start_tap` does (an `SCShareableContent` lookup) crashed
   with `SIGSEGV` at full speed, every time — but *never* crashed
   running under `lldb`, which only ever adds latency. That's the
   signature of losing a race against an async handshake, not a memory
   bug, and it's exactly the "one-time initialization race" this
   section used to describe as "not yet root-caused." Apple exposes no
   synchronous "wait until ready" hook for it, so the fix is a one-time,
   150ms settle delay after first establishing app context (paid once
   per process, on the first tap start only).

Both were invisible without a real native debugger: Go's crash printer
correctly pointed at `guestaudio_start_tap` both times, but the *why*
(a libdispatch deadlock detector; a race that only a debugger's slowdown
happened to avoid) only showed up in `lldb`'s fully symbolicated
backtrace. Re-validated clean (401 callbacks / 384,960 samples / 8.0s at
48kHz, matching the original standalone validation) across multiple
consecutive runs at full speed, no debugger, real Screen Recording
permission grant (not partial/inherited).

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

### Phase 2 — meeting mode

**Dual-source audio capture: done.** New `internal/meetingaudio`
package (`NewMonitorSource(deviceHint string) (*audio.StreamCapturer,
error)`) is meeting mode's single, OS-agnostic second-audio-source
entry point, called from both `internal/daemon` and `internal/backend`
where they used to build the monitor capturer inline:

- Linux (`meetingaudio_linux.go`): today's existing PulseAudio-monitor
  logic, moved here verbatim — zero behavior change, a real
  construction error still stays fatal.
- macOS (`meetingaudio_darwin.go`): ignores `deviceHint` (no
  PulseAudio-shaped device concept exists) and always calls
  `teamsvideo.FindMeetingWindow()`, capturing that window's audio via a
  new `internal/guestaudio.WindowCapturer` — a thin adapter giving
  `Tap`'s push-based callback the same `Start`/`Stop`/`Samples`/
  `Reset`/`Close` shape `internal/audio.Capturer` expects (structural
  typing, no import needed), resampling ScreenCaptureKit's 48kHz down
  to `audio.CaptureSampleRate` (new exported constant; new
  `audio.Resample` in `dsp.go`, linear interpolation) since nothing
  downstream resamples on its own. Unlike Linux, **no failure here is
  ever fatal** — no window found, or the tap failing to start, both
  fall back to mic-only with a printed notice, since finding an active
  meeting window is inherently probabilistic and mic-only is still a
  fully-working session.

Verified live end-to-end against a real Teams window: hotkey → dual
mic+guest-audio capture → real transcription → clean stop → saved
session with `"sources": ["mic", "monitor"]`. See the guestaudio section
above for the two real bugs this integration surfaced and fixed
(`dispatch_once` self-deadlock; a window-server handshake race) — both
was root-caused with `lldb`, not worked around.

`internal/teamsvideo`'s pre-existing `go vet` advisory
(`possible misuse of unsafe.Pointer` in `window_darwin.go`) is also now
fixed — a small `cfarray_is_null` C helper does the `NULL` check instead
of a Go-side `unsafe.Pointer` comparison. `go vet ./...` is fully clean
on darwin now, no scoping needed.

1. **GUI live transcription for meeting mode on macOS: done.** Verified
   live: the transcript pane streams real segments as they arrive
   (Wails `transcript:segment` events → React, unchanged from Linux —
   `internal/backend/events.go`'s `emitSegments` doesn't know or care
   which OS captured the audio), with a dual mic+guest-audio session
   against a real Teams window, saved with `"sources": ["mic",
   "monitor"]`. One real bug found and fixed along the way, in scope
   for this same verification pass: the toolbar's "System Audio"
   picker showed a misleading **"No System Audio"** even while guest
   audio was actively capturing, because it was built entirely around
   Linux's PulseAudio-monitor-device-list concept, which is always
   empty on macOS. New `App.SystemAudioMode()` (`"auto"` on darwin,
   `"manual"` on Linux) lets `SourceSelector.tsx` render an honest
   "System Audio: Auto-detect" label on macOS instead of a picker with
   nothing to pick.

**Not yet built** — the actual point of the original architecture doc
(video hint → speaker cluster labeling) and everything after it:

1. **Live clustering/diarization, tightened for two sources.**
   `internal/speaker`'s embedding+clustering already runs live today
   (per-segment, as audio arrives, on both platforms) — this isn't
   building live clustering from scratch. The macOS-specific work is
   making sure clustering behaves well when mic and guest audio are two
   *separate* streams captured differently (a ScreenCaptureKit window
   tap vs. a PulseAudio monitor source can have different noise floors,
   gain staging, and dropout patterns), which could bias which
   embeddings get clustered together in ways Linux's single
   monitor-source input never has to handle.
2. **Screen-based speaker-label hints via rules, with an escalation
   path for unrecognized UIs.** Generalizes today's
   Teams-window-shaped `internal/teamsvideo` heuristic (owner name +
   title matching; ring-color/shape detection and Vision.framework OCR
   for the name label are both still unbuilt) into a rule table keyed
   by meeting app (Teams, Zoom, Meet, Webex, Slack — the same set
   `internal/meeting/platform.go` already recognizes by window title on
   the detection side) instead of one hardcoded Teams-only path. The
   escalation path matters as much as the rules themselves: when a
   window doesn't match any known app's rules, log it (app name,
   window title shape) instead of guessing, and keep clustering-only
   "Person N" labeling — exactly like a hint-less cluster already
   falls back today. That log is how the rule table grows over time
   without silently mislabeling someone in the meantime.
3. **Persistent voiceprints.** A speaker cluster's embedding centroid
   *is* a voiceprint (noted in this doc's original architecture
   section) — this item is giving it a durable identity: once a video
   hint resolves a cluster to a real name, store that centroid keyed by
   name (not just for the current session), so a returning speaker
   gets recognized from voice alone in a *future* meeting, without
   needing a fresh video hint every time. Needs a storage location
   (likely alongside `internal/config`'s data dir) and a
   similarity-threshold policy for matching a new session's cluster
   against stored voiceprints — reusing `speaker.DefaultThreshold`'s
   general shape, but this is cross-session matching, not within-session
   clustering, so it may warrant its own threshold.
4. **Persistent face signatures.** The video-side counterpart to #3:
   store a face embedding (or at minimum a representative thumbnail)
   keyed by the same resolved name, from the same video-hint moment
   that already reads a name label. This is what lets a familiar face
   get identified even in the window before OCR reads *this* session's
   name label — e.g. a participant whose tile briefly shows no label,
   or joins with camera on but hasn't been named by Teams' UI yet.
   Depends on #2 existing first (need a real face crop from the video
   hint pipeline to embed).
5. **Replay-based test harness.** The ability to feed a *recorded*
   meeting (audio, and once #2-#4 exist, screen capture too) through
   the whole pipeline offline — capture once against a real or staged
   call, then replay it repeatedly against pipeline changes without
   needing a live call every time. This is what makes #1-#4
   regression-testable at all; right now the only way to validate any
   of this is a live call, which is exactly why this session's guestaudio
   bugs took real debugging effort to catch (see above) rather than
   showing up in `go test`.

## Background

The video-ring/OCR mechanism, the ScreenCaptureKit gotchas, and the full
evidentiary trail (why AXUIElement was tried and disproven first, how the
planar-audio bug was actually diagnosed, live test numbers against real
calls) live in a separate internal research repo,
`anielsud/tomoe-darwin` — a Python/pyobjc spike, not something this
package depends on or ports code from directly. Treat it as prior art for
*why* these mechanisms work, not as a second implementation to keep in
sync with this one.
