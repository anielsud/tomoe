# macOS audio + window capture

Covers `internal/teamsvideo` (window discovery/frame capture),
`internal/guestaudio` (per-window/system audio tap), `internal/audiosources`
(active-audio-process enumeration), and `internal/meetingaudio`'s darwin
implementation (wiring the above into meeting mode's second audio
source). See [`macos-support.md`](macos-support.md) for the overall
status/roadmap and [`macos-video-hints.md`](macos-video-hints.md) for
what's built on top of window capture.

## `internal/teamsvideo` — window discovery + frame capture

`FindMeetingWindow()` and `CaptureWindowRGB()` use CoreGraphics
(`CGWindowListCopyWindowInfo`, `CGWindowListCreateImage`) — pure C API,
no Objective-C shim needed.

- **`FindMeetingWindow`'s heuristic:** a "Microsoft Teams"-owned window
  whose title is non-empty, doesn't start with `"Chat |"`, and isn't
  `"Window"`. This is necessary but **not sufficient** — see
  `macos-video-hints.md`'s call-chrome gate for two real windows this
  heuristic alone let through that weren't actually calls (a chat tab,
  a post-meeting recording/playback page).
- **`CaptureWindowRGB` survives occlusion.** It targets a specific
  `CGWindowID` with `kCGWindowImageBoundsIgnoreFraming`, reading the
  window-server's compositor buffer directly rather than a screen
  region — confirmed live: capture still returns a correctly-sized
  frame even with the window minimized.
- **Known risk:** `CGWindowListCreateImage` is marked `obsoleted` (not
  just deprecated) as of the macOS 15 SDK — a hard compile error unless
  the cgo build pins `-mmacosx-version-min=14.0`. Still works at
  runtime on current macOS, but Apple could remove the symbol outright.
  The durable fix is porting frame capture to ScreenCaptureKit (the
  same framework `internal/guestaudio` already uses for audio) — not
  something to defer indefinitely.

### `FindWindowForPID` — resolving a specific app's window

Added for the audio-source picker (see below): given a PID that
CoreAudio reports as actively producing audio, find the window to tap.
Two real bugs found here, both discovered by direct Go-level test
programs against a real, live Teams call rather than guesswork:

1. **The audio-active PID is often a helper process with no window of
   its own.** CoreAudio reported Teams' actual audio-producing PID as
   "Microsoft Teams WebView" (`com.microsoft.teams2.helper`) — a
   Chromium-style renderer subprocess, not the main process that owns
   the visible window. Fixed by walking up the parent-process chain
   (`ps -o ppid=`, up to 5 levels) until an ancestor that owns a window
   is found.
2. **The resolved PID can own several windows; "largest" picks the
   wrong one.** Teams' main process can own a Calendar tab, a chat
   panel, *and* the actual call simultaneously — in one real test the
   Calendar tab's window was larger than the meeting window. Fixed by
   checking whether the resolved owner's name contains "teams" and, if
   so, delegating to `FindMeetingWindow`'s own title heuristic first,
   only falling back to "largest window" generically otherwise.
3. **Not every audio-active process is even a descendant of the
   window-owning one.** A later live session hit a *different* Teams
   helper ("modulehost") whose parent PID was 1 (launchd) — a
   launchd-spawned XPC service, not a child of Teams at all, so the
   ancestor walk terminated after one hop and silently fell back to
   mic-only. Fixed at the `internal/meetingaudio` layer instead of here
   (see `isTeamsPID` below): once the audio source's own app
   identity (name/bundle ID) says "this is Teams," skip PID/window
   ancestry matching entirely and go straight to `FindMeetingWindow`.

**Porting lesson:** don't trust process ancestry to relate "the PID an
OS API reports as active" to "the PID that owns the relevant window" —
for any non-trivial (multi-process, sandboxed/XPC) app, match by app
identity (bundle ID / name) instead, and only fall back to structural
matching (windows, PIDs) for apps with no dedicated finder.

## `internal/guestaudio` — ScreenCaptureKit audio tap

`SCStream`-based tap, scoped either to one window
(`SCContentFilter initWithDesktopIndependentWindow:`) or the whole
display (`initWithDisplay:excludingWindows:@[]]`, the "Everything"
source-picker option). Objective-C shim (`bridge_darwin.m`) implements
`SCStreamOutput`; Go only sees raw sample buffers via a `//export`'d
callback.

- **Known quirk: planar, not interleaved, stereo.** ScreenCaptureKit
  delivers all of channel 0 then all of channel 1, not interleaved
  samples. The downmix averages the two channel halves — get this
  wrong and audio decodes to fluent-sounding transcription that's
  semantically nonsense, not a crash, which makes it easy to miss.
- **Two real bugs, both root-caused via `lldb`'s symbolicated
  backtrace** (Go's own crash printer only shows Go-visible frames —
  it correctly pointed at the right cgo call both times, but not the
  actual fault):
  1. **`dispatch_once` self-deadlock.** `guestaudio_ensure_app_context()`
     always hopped `[NSApplication sharedApplication]` over to the main
     queue via `dispatch_sync`, on the assumption the caller might not
     already be on the main thread. A caller that *is already* the
     main thread deadlocks — `dispatch_sync` to the queue you're
     already running on is a same-thread deadlock, and libdispatch
     traps rather than hanging (backtrace: `guestaudio_start_tap` →
     `dispatch_once_callout` → `dispatch_sync_f_slow` →
     `DISPATCH_WAIT_FOR_QUEUE`). Fixed by checking `[NSThread
     isMainThread]` first and only hopping when actually needed.
  2. **A real window-server/XPC handshake race.** `[NSApplication
     sharedApplication]` kicks off the process's first window-server
     connection but doesn't block until it's ready. The very next call
     (`SCShareableContent` lookup) crashed with `SIGSEGV` at full
     speed, every time — but never under a debugger, which only adds
     latency: the signature of losing a race against an async
     handshake, not a memory bug. No synchronous "wait until ready"
     hook exists, so the fix is a one-time settle delay after
     establishing app context (paid once per process). **Recurred
     later at 150ms** (a fresh process's first "Everything"-mode tap
     start crashed once, succeeded on immediate retry) — bumped to
     500ms, verified 5/5 fresh-process attempts survive at that value
     vs. a reproducible crash before. Porting lesson: don't trust a
     "verified clean" settle-delay value that was only tested a few
     times — this kind of race can pass repeatedly at an insufficient
     delay purely by luck.
- **A Go slice's backing array isn't safe to hold by raw pointer
  across a completion handler that outlives the call.** (Same lesson
  independently re-learned in the OCR shim — see
  `macos-video-hints.md`.)

## `internal/audiosources` — "who's making sound right now"

`ListActive()` enumerates every process currently producing audio
output via `kAudioHardwarePropertyProcessObjectList` +
`kAudioProcessPropertyIsRunningOutput` — a synchronous CoreAudio
property query, confirmed reliable and **not** the same API as the
CoreAudio Process Tap *capture* mechanism (a different, separately
cgo-bindable API that a prior research spike found builds successfully
but never actually delivers a callback with data — a documented dead
end, not attempted here). Also resolves each process's display name
(`NSRunningApplication`) and bundle ID
(`kAudioProcessPropertyBundleID`), used both for the source picker's
labels and for app-identity matching (see `isTeamsPID` below).

## `internal/meetingaudio` (darwin) + the audio-source picker

`NewMonitorSource(sourceHint string)` is meeting mode's single
platform-agnostic second-audio-source entry point. On macOS,
`sourceHint` is either:

- `"everything"` — the whole system's audio via
  `guestaudio.NewSystemCapturer()`, speaker diarization skipped
  (`live.Config.SkipMonitorDiarization`) since it's not one app's
  isolated stream — labeled `"System Audio"` rather than clustered.
- a decimal PID from `audiosources.ListActive()` — that app's audio via
  a window it owns. Resolved by app identity, not process ancestry:
  `isTeamsPID` checks the source's own name/bundle ID from
  `audiosources.ListActive()` and, if it's Teams, calls
  `FindMeetingWindow` directly; anything else falls back to
  `FindWindowForPID`'s generic PID/window matching. Diarization stays
  on for this path.

Unlike Linux, **no failure here is ever fatal** — no window found, a
stale PID, a Tap failing to start (most commonly a missing Screen
Recording grant) — all fall back to mic-only with a printed notice,
since finding an active meeting window is inherently probabilistic and
mic-only is still a fully-working session.

This replaced an earlier, non-functional "System Audio: Auto-detect"
label that always showed even while guest audio was actively
capturing — a leftover from `internal/backend`'s `SourceSelector.tsx`
having been built entirely around Linux's PulseAudio-monitor-device
concept, which is always empty on macOS.

**Also fixed while wiring meeting-mode audio:** the ScreenCaptureKit
→16kHz resample (`internal/guestaudio/capturer_darwin.go`) used plain
linear interpolation with no anti-aliasing filter — for an exact 3:1
downsample, that folds frequency content above the new Nyquist limit
back into the audible band as noise, degrading exactly the input
`internal/speaker`'s embedding model relies on to tell voices apart.
Fixed with a cascaded `audio.LowPassFilter` applied in `Resample`
before decimating (Linux's PulseAudio monitor never resamples at all,
so this was macOS-only). Verified with a unit test showing a 20kHz tone
(above 16kHz's 8kHz Nyquist) comes through heavily attenuated rather
than aliased-and-preserved.
