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

**Live clustering, tightened for two sources: done** — though not for
the reason this section originally guessed. `internal/live/pipeline.go`'s
`assignSpeaker` turned out to route mic audio straight to `"You"`,
bypassing `internal/speaker`'s embedder/tracker entirely — only the
monitor/guest-audio source ever reaches clustering, on *both*
platforms, so mic vs. guest audio being separately-captured streams was
never actually a clustering risk to begin with. The real, concrete bug:
`internal/guestaudio/capturer_darwin.go`'s ScreenCaptureKit→16kHz
resample (added wiring up meeting-mode audio) used plain linear
interpolation with **no anti-aliasing filter** — for an exact 3:1
downsample, that folds frequency content above the new Nyquist limit
back into the audible band as noise, degrading exactly the input
`internal/speaker`'s embedding model relies on to tell voices apart.
Linux's PulseAudio monitor never resamples at all, so this was a
real, macOS-only quality gap. Fixed with a new `audio.LowPassFilter`
(cascaded a few passes for a properly steep anti-aliasing cutoff, since
one pole alone only rolls off -6dB/octave) applied in `Resample` before
decimating. Verified: a unit test demonstrating a 20kHz tone (above
16kHz's 8kHz Nyquist) comes through heavily attenuated rather than
aliased-and-preserved, plus a live re-run of the dual-source meeting
flow against a real Teams window confirming no regression.

**Not yet built** — the actual point of the original architecture doc
(video hint → speaker cluster labeling) and everything after it:

1. **Screen-based speaker-label hints via rules, with an escalation
   path for unrecognized UIs.** Split into three PRs; this is PR A of
   three, **done**:
   - New `internal/videohint` package: a `Rule`/`RingConfig` table
     keyed by `meeting.Platform`, and a real, unit-tested
     connected-components ring-detection algorithm
     (`DetectRing` — color-threshold → flood-fill labeling → hollow
     rectangular-border check → size-fraction filtering). The rule
     table starts **intentionally empty** — verifying real ring
     color/shape thresholds needs either live access to an active
     multi-participant call in each app, or reviewed examples from the
     escalation library below, neither of which existed going in. Every
     platform, Teams included, escalates today rather than risk
     confidently mislabeling a real meeting from an unverified rule.
   - An escalation path wired into meeting mode's start/stop
     (`internal/daemon`, `internal/backend` — a `videohint.Poll` goroutine
     scoped to the session, cancelled on stop, no-op on Linux) that
     captures a frame + metadata whenever it doesn't have a confident
     hint to offer.
   - **A real privacy bug found live while testing this, before it
     shipped:** the escalation target was originally meant to be a
     permanent library, but `internal/teamsvideo.FindMeetingWindow`'s
     existing heuristic (owner name contains "teams" + a non-trivial,
     non-"Chat |" title) matches more than actual call windows — it
     matched a plain 1:1 chat tab during testing and captured a real
     private conversation. Fixed by never treating a capture as safe:
     escalation now lands in a **staging** area
     (`config.UnrecognizedUIPendingDir`), and nothing reaches the
     permanent library (`config.UnrecognizedUIApprovedDir`) without a
     human explicitly approving it — new `tomoe videohint {list,approve,discard}`
     CLI commands. This is the actual reason the rule table has to stay
     manually curated rather than auto-populating from captures: the
     matched window can be the wrong thing entirely.
   - Verified live: a real Teams-window capture went through the full
     staging → `videohint list` → `videohint discard` path with nothing
     ever touching the permanent library; confirmed no regression in
     the existing dual-source audio/transcription flow.

   PR B of three, **done**: calibrated Teams' rule table entry against
   a real, live, multi-participant Teams call (explicit authorization
   obtained first), plus a Vision.framework OCR shim for the name
   label itself.
   - Real measured values: the active-speaker ring is an
     RGB(129,136,243) hollow rounded-square border (`ColorTolerance:
     25`, generous on purpose to survive lighting/monitor variation);
     the participant's name label overlays the **bottom ~27% of the
     ring's own bounding box**, not a separate region below it — an
     empirical finding from cropping real frames, not an assumption
     (`internal/videohint/rule.go`'s `LabelRegion`,
     `internal/videohint/label.go`'s `LabelRect`/`RecognizeLabel`).
   - New Vision.framework OCR shim
     (`internal/videohint/ocr_bridge.h`/`ocr_bridge_darwin.m`/
     `ocr_darwin.go`, no-op `ocr_linux.go`), following the same
     cgo/Objective-C bridge shape as `internal/guestaudio`. **A real
     crash found live while wiring this up:** this file builds without
     ARC (manual retain/release, matching the rest of this project's
     ObjC bridges), and Vision hands the completion handler's block
     autoreleased objects — assigning one straight into a `__block`
     variable without retaining it let it get freed before the outer
     function read it back, segfaulting on every call. Fixed by
     explicitly retaining in the block and releasing before return.
     Also switched frame handling from `CGDataProviderCreateWithData`
     (wraps the caller's pointer directly) to a `CGDataProviderCreateWithCFData`
     over an immediately-copied `NSData`, since holding a Go slice's
     backing array by raw pointer across the cgo boundary isn't safe.
   - `videohint.Poll` now actually attempts OCR when Teams' ring
     matches, instead of always escalating: a recognized name is
     reported as a naming hint via `Poll`'s new `events` channel (see
     PR C below — this replaced an earlier de-duplicated-`Printf`
     version once there was a real consumer for a structured event);
     no ring match, no configured label region, or empty OCR output
     all still fall through to the existing escalation path unchanged.
   - Verified against real cropped frames from the live call (deleted
     after use, never committed): OCR correctly read a real
     participant's name back from both a tight label-only crop and a
     wider ring+label crop.

   PR C of three, **done**: wiring a successful ring+OCR result into
   actually relabeling an `internal/speaker.Tracker` cluster live, plus
   a GUI activity trace so the app shows not just *what* it's doing but
   *how and when*.
   - `videohint.Poll` gained an `events chan<- Event` parameter: one
     `Event` per pipeline stage it reaches each tick (window
     found/not, frame captured, ring matched/not, OCR hit/miss,
     escalated), non-blocking so a slow/absent consumer never stalls
     polling. `speaker.Tracker` gained `SetHintForRecent(name,
     maxAge)`: since a video hint only knows "this name is active
     right now," not which cluster ID it belongs to, it's attributed
     to whichever cluster the audio pipeline most recently assigned an
     embedding to (both signals are keyed to the same monitor-source
     audio) — the resulting label is `"Person N (Name)"`, baked in at
     `Tracker.Assign` time, so only turns transcribed *after* a hint
     lands show the name; earlier turns correctly keep the plain
     `"Person N"` they were labeled with at the time.
   - `internal/daemon` and `internal/backend` each drain their own
     `events` channel: the CLI daemon logs every stage (consistent
     with its existing verbose-logging stance); the GUI additionally
     buffers a short ring of recent events (`GetVideoHintActivity`, for
     a panel opened mid-session) and forwards each one live via a new
     `"videohint:activity"` Wails event.
   - New `VideoHintActivity` frontend component: a collapsed one-line
     ticker (latest stage + relative time) above the transcript, only
     rendered on macOS (`systemAudioMode === 'auto'`), expandable into
     a scrolling log of the last 50 stages.
   - Verified live against a real, active, multi-participant Teams
     call: watched the ticker report real stages in real time
     (`no ring match found` → `OCR read "Nazanin Rame…" from the label
     region`), and confirmed the transcript pane actually rendered
     `Person 2 (Nazanin Rame...):` and `Person 3 (Per Gunsarfs):` once
     those hints landed, with earlier turns from the same speakers
     correctly left as plain `Person N`.

   Follow-up fixes from a second live test pass, same day: real
   multi-participant testing surfaced three concrete gaps in the above.
   - **Confirmed (no code change needed): window capture already
     survives occlusion.** `teamsvideo.CaptureWindowRGB` uses
     `CGWindowListCreateImage` targeting a specific `CGWindowID` with
     `kCGWindowImageBoundsIgnoreFraming` — it reads the window-server's
     compositor buffer directly, not a screen region, so it doesn't
     need the window frontmost or even visible. Verified live: minimized
     the real Teams window and confirmed `CaptureWindowRGB` still
     returned a correctly-sized frame.
   - **Speaker clustering was fragmenting one person's continuous turn
     into a fresh "Person N" per sentence** — a real bug hit live
     testing (a participant's speech split across many single-sentence
     VAD segments, each producing a noisier embedding than a longer
     utterance, and just missing `speaker.Tracker`'s similarity
     threshold often enough to register as a new speaker almost every
     time). Fixed with a "sticky speaker" continuity heuristic
     (`stickyGraceWindow`/`stickyThresholdMargin` in
     `internal/speaker/cluster.go`): a near-miss similarity is still
     accepted as the same speaker if the best-matching centroid is also
     whoever was assigned moments ago — without folding the near-miss
     embedding into that centroid, so a run of noisy sentences can't
     drag a good centroid toward a bad one.
   - **OCR was only ever attempted on `videohint.Poll`'s fixed
     10-second tick**, so a newly-heard, still-unlabeled speaker could
     wait up to 10s for their first naming attempt. `Poll` now also
     takes a `trigger <-chan struct{}` (debounced separately from the
     ticker via `minAttemptInterval`); `speaker.Tracker.Assign` reports
     whether the assigned speaker still has no hint, and
     `live.Coordinator.HintNeeded()` signals `trigger` the moment one
     is heard — cutting a still-unknown speaker's first OCR attempt
     from up to 10s down to a few seconds.
   - Added a face-bubble thumbnail alongside the OCR'd text: a
     `StageOCRHit` `Event` now also carries a PNG crop of the matched
     ring's own bounding box (`RingThumbnailPNG` in
     `internal/videohint/label.go` — the participant's video tile
     itself, not just their name label), so the activity ticker/log
     shows *who* was recognized next to the text, not just the text
     alone. Verified live: a real thumbnail rendered correctly in the
     ticker.
   - Still open: window-finding for Zoom/Meet/Webex/Slack, none of
     which `internal/videohint` can capture anything for yet
     (Teams-only, reusing `FindMeetingWindow` as-is); `DetectRing`
     returns only its single best-scoring match, so a frame with two
     simultaneous rings (observed live — Teams can highlight more than
     one recent speaker at once) currently only produces a hint for
     one of them; a hint with no recent-enough speaker to attach to
     (`SetHintForRecent` returns `false`) is logged but otherwise
     silently dropped, not retried; the sticky-speaker margin
     (`stickyThresholdMargin = 0.15`) is a single hand-picked constant,
     not tuned against a real dataset, and could in principle merge a
     genuine quick speaker change if the new speaker's embedding
     happens to still score closest to whoever spoke immediately
     before them.
2. **Persistent voiceprints.** A speaker cluster's embedding centroid
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
3. **Persistent face signatures.** The video-side counterpart to #2:
   store a face embedding (or at minimum a representative thumbnail)
   keyed by the same resolved name, from the same video-hint moment
   that already reads a name label. This is what lets a familiar face
   get identified even in the window before OCR reads *this* session's
   name label — e.g. a participant whose tile briefly shows no label,
   or joins with camera on but hasn't been named by Teams' UI yet.
   Depends on #1 existing first (need a real face crop from the video
   hint pipeline to embed).
4. **Replay-based test harness.** The ability to feed a *recorded*
   meeting (audio, and once #1-#3 exist, screen capture too) through
   the whole pipeline offline — capture once against a real or staged
   call, then replay it repeatedly against pipeline changes without
   needing a live call every time. This is what makes #1-#3
   regression-testable at all; right now the only way to validate any
   of this is a live call, which is exactly why this session's guestaudio
   bugs took real debugging effort to catch (see above) rather than
   showing up in `go test`.
5. **Two-pass transcription: realtime + a higher-fidelity re-pass.**
   Today's transcript is single-pass — whatever Parakeet TDT streams
   live during the meeting is the final text, forever. The ask is a
   second pass, closer to Whisper's non-streaming/chunked style, that
   revisits completed audio afterward with a larger context window
   and/or a heavier model, upgrading the low-latency live line to a
   more accurate final one without blocking the live view. Not built:
   `internal/session` already stores each session's raw audio (M4A)
   specifically so a later re-transcription is possible in principle
   (`tomoe transcribe`/`RetranscribeSession` even exist as a *manual*,
   whole-file batch path today), but there's no automatic background
   second pass that revisits a session's segments as it goes, and no
   UI distinction between "live, may still be refined" and "final."
6. **Full participant names, not just what's visible in a partial UI
   label.** Teams' active-speaker tile often truncates the name (e.g.
   "Nazanin Rame…", cut off by the tile's width) — a fine naming hint,
   not necessarily the participant's actual full name. Resolving the
   truncation needs a second signal: reading the roster/participants
   panel (a different, richer piece of Teams' UI than the
   active-speaker tile) or an org-directory lookup once a partial name
   is known. Not built — `internal/videohint` has no roster-reading
   capability today, only the active-speaker tile+label.
7. **In-call chat captured as transcript asides.** A meeting app's text
   chat is a parallel channel Tomoe currently can't see at all —
   messages posted mid-call (links, corrections, side comments) carry
   real context a speech-only transcript misses. The ask is to capture
   that chat stream and store it in the session transcript as
   timestamped asides alongside the spoken segments, visually
   distinguishable from speech. Needs its own capture mechanism (most
   likely another rule-driven screen-read of the chat panel, in the
   same spirit as `internal/videohint`'s ring/label reading, since
   there's no known stable API to read a live Teams chat from outside
   the app) plus a `session.Segment`-adjacent data shape for a
   non-speech aside. No design work has started on this yet.

## Background

The video-ring/OCR mechanism, the ScreenCaptureKit gotchas, and the full
evidentiary trail (why AXUIElement was tried and disproven first, how the
planar-audio bug was actually diagnosed, live test numbers against real
calls) live in a separate internal research repo,
`anielsud/tomoe-darwin` — a Python/pyobjc spike, not something this
package depends on or ports code from directly. Treat it as prior art for
*why* these mechanisms work, not as a second implementation to keep in
sync with this one.
