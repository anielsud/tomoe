# macOS Support (in progress)

**Status:** `cmd/tomoe` (CLI dictation), `cmd/tomoe-gui`, and meeting
mode's dual-source audio capture all build, run, and are verified
end-to-end on real macOS hardware. Video-hint speaker labeling (reading
Teams' active-speaker ring + name label and attaching a real name to
an audio cluster) is also built and verified live — a cluster with no
video hint still falls back to "Person N", exactly like Linux.
Automatic meeting detection has no macOS implementation yet. See
[Roadmap](#roadmap) for exactly what's left.

Detailed, porting-relevant implementation notes (real bugs found, root
causes, calibration data) live in three companion docs so this one
stays a readable entry point:

- [`macos-audio-capture.md`](macos-audio-capture.md) — window
  discovery/capture, ScreenCaptureKit audio taps, active-audio-process
  enumeration, the audio-source picker.
- [`macos-video-hints.md`](macos-video-hints.md) — reading Teams' UI to
  label speaker clusters with real names.
- [`macos-build.md`](macos-build.md) — CLI/GUI build fixes, tray/window
  coexistence, code signing and TCC permission persistence.

## Why this isn't a straight port

Every other cross-platform package (`hotkey`, `clipboard`, `notify`) is
a 1:1 swap: same interface, new backend. Speaker naming isn't, because
the input signal is structurally different per platform:

| | Linux | macOS |
|---|---|---|
| Meeting detection | PulseAudio: simultaneous source-output + sink-input from one PID | not yet built (no equivalent signal wired up — see [Roadmap](#roadmap)) |
| Speaker naming signal | none — audio embeddings only | Teams' active-speaker ring + name label (visual, OCR'd) |
| Speaker naming | `internal/speaker` clusters embeddings → "Person N" | same clustering, unchanged, plus a cluster can be *labeled* by a video hint |

Speakers are separated by voice the same way on both platforms (by
default, Tomoe's own diarizer running during the meeting; see
[`speaker-pipeline-design.md`](speaker-pipeline-design.md)). macOS adds a
second input: timestamped name reads from Teams' window, which name the
timeline's speakers by vote and constrain its clustering. A speaker that
never gets agreeing reads (no video signal, or a dial-in participant with
no tile) still gets labeled "Person N". This also
gives persistent per-person voiceprints a natural home later (see
[Roadmap](#roadmap) item 1): a cluster's embedding centroid *is* the
voiceprint, keyed to whatever name a hint resolved it to.

## Architecture

```text
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
other and of the voice pipeline. The diagram shows the original design;
today names are timestamped hints attributed by vote against the
diarization timeline (`internal/diarize`), and only with
`diarize_during_meeting = false` attached to the live tracker's most
recent speaker (`speaker.Tracker.SetHintForRecent`). See
`macos-video-hints.md`.

## Also shipped, not macOS-specific

**Two-pass transcription (realtime + higher-fidelity re-pass).**
Motivated by macOS testing (Parakeet TDT's offline-only decode meant
nothing appeared on screen until a pause, which stood out badly during
live meeting testing) but lives in `internal/transcribe`/`internal/live`
with no OS-specific code — see `CLAUDE.md`'s "Two-pass transcription"
entry for the mechanism. Two gotchas worth keeping visible here since
they're easy to miss from the code alone:

- Getting the first ("live", growing-partial) state right needed a
  speaker label attached to it *before* the utterance finishes: the
  first 0.5s of audio is buffered silently so there's enough signal
  for a real embedding, a speaker is assigned once from that, and
  every later partial reuses it under the same segment ID rather than
  re-assigning (which risked flicker and double-counting the utterance
  into the speaker tracker's centroid).
- `Coordinator.Stop()` returns as soon as its pipelines finish, not
  after background refinement (pass 2) fully drains — matching this
  codebase's existing "stop returns immediately, save runs async"
  design elsewhere. If a save runs before the last segment or two
  finish refining, the persisted session keeps their pass-1
  (still-usable, just less-refined) text. Pre-existing class of race,
  not introduced by this feature, not chased further.

## Roadmap

Ordered roughly by dependency, not priority — later items build on
earlier ones.

1. **Persistent voiceprints.** A speaker cluster's embedding centroid
   *is* a voiceprint already — this item gives it a durable identity:
   once a video hint resolves a cluster to a real name, store that
   centroid keyed by name (not just for the current session), so a
   returning speaker is recognized from voice alone in a *future*
   meeting without needing a fresh video hint every time. Needs a
   storage location (likely alongside `internal/config`'s data dir)
   and a similarity-threshold policy for matching a new session's
   cluster against stored voiceprints — reusing
   `speaker.DefaultThreshold`'s general shape, but this is
   cross-session matching, not within-session clustering, so it may
   warrant its own threshold.
2. **Persistent face signatures.** The video-side counterpart to #1:
   store a face embedding (or at minimum a representative thumbnail)
   keyed by the same resolved name, from the same video-hint moment
   that already reads a name label. Lets a familiar face get
   identified even in the window before OCR reads *this* session's
   name label — e.g. a participant whose tile briefly shows no label,
   or joins with camera on but hasn't been named by Teams' UI yet.
   Depends on #1 existing first (need a real face crop from the video
   hint pipeline to embed).
3. **Replay-based test harness.** Feed a *recorded* meeting (audio, and
   once #1-#2 exist, screen capture too) through the whole pipeline
   offline — capture once against a real or staged call, then replay
   it repeatedly against pipeline changes without needing a live call
   every time. This is what would make #1-#2 (and the existing
   video-hint work) regression-testable at all; right now the only way
   to validate any of this is a live call, which is exactly why real
   bugs in this area have taken live debugging sessions to catch
   rather than showing up in `go test`.
4. **Full participant names.** Teams' active-speaker tile often
   truncates the name (e.g. "Nazanin Rame…", cut off by the tile's
   width) — a fine naming hint, not necessarily the participant's
   actual full name. Resolving the truncation needs a second signal:
   reading the roster/participants panel (a different, richer piece of
   Teams' UI than the active-speaker tile) or an org-directory lookup
   once a partial name is known. Not built — `internal/videohint` has
   no roster-reading capability today, only the active-speaker
   tile+label.
5. **In-call chat captured as transcript asides.** A meeting app's text
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
   non-speech aside. No design work started.
6. **Automatic meeting detection.** No macOS equivalent of Linux's
   PulseAudio dual-stream signal exists — meeting mode has to be
   started manually today. Explicitly deferred (not started) but
   scoped: `internal/audiosources.ListActive()` (built for the
   audio-source picker) already reports when a known meeting app starts
   producing audio output, which is the same signal Linux's detector
   uses in spirit (mic+speaker activity from one PID) — reusing it here
   is the intended path, not a new detection mechanism.
7. **Window-finding for Zoom/Meet/Webex/Slack.** `internal/videohint`
   only has a window-finder for Teams today; see
   `macos-video-hints.md`'s "Still open" for what each platform would
   need.

## Linux and macOS: where they differ

A review of `feature/transcript-quality` (2026-10-02). "Gap" means one
platform lacks something the other has; the rest are different but
equivalent approaches. None of the Linux-side gaps change Linux
transcripts: they are macOS-only additions Linux never had.

**Missing on macOS**

- Automatic meeting detection (`internal/meeting/detect_darwin.go` is a
  stub, so `auto_detect = true` does nothing) and, for browser meetings,
  platform identification (Linux reads the window title with `xdotool`).
- GPU transcription: macOS is CPU-only; the CUDA path (`internal/gpu`,
  `config.EnsureGPULibs`, `make install-gpu`) is Linux-only.
- Video hints cover Teams only (item 7 below), and tile names are
  truncated (item 4).

**Missing on Linux (macOS-only additions)**

- Video-based speaker naming (`internal/teamsvideo`, `internal/videohint`
  OCR and ring reading, the hint timeline, `looks.jsonl`): Linux only
  gets "Person N" from voice clustering.
- System audio choices: "Meeting app (automatic)", "Everything" and the
  active-app list (`internal/audiosources`). Linux captures a PulseAudio
  monitor device chosen by name.
- App bundle packaging, permission checks (Microphone, Screen Recording,
  Accessibility) and the crash log at `~/Library/Logs/Tomoe/`.
- In the other direction, Linux has no Wayland global hotkey (X11
  `XGrabKey` only), and no `.desktop` file, `.deb` or autostart.

**Different but equivalent**

| | Linux | macOS |
|---|---|---|
| System audio capture | PulseAudio `.monitor` source; failure is fatal | ScreenCaptureKit (resampled and low-passed); failure falls back to mic only |
| Global hotkey | X11 `XGrabKey` | Carbon `RegisterEventHotKey` |
| Typing dictation | `xdotool type` / `wtype` | `osascript` keystrokes (needs Accessibility) |
| Notifications | `notify-send` | `osascript display notification` |
| Diarizer priority | nice +10 | `QOS_CLASS_UTILITY` |
| Tray run loop | goroutine | Cocoa main thread |
| Setup checks | tools list with `apt` fixes | permissions list, `brew` ffmpeg |
| Data and config paths | `~/.config/tomoe`, `~/.local/share/tomoe` (XDG) | the same paths, not `~/Library/Application Support` |

## Background

The video-ring/OCR mechanism, the ScreenCaptureKit gotchas, and the
full evidentiary trail (why `AXUIElement` was tried and disproven
first, how the planar-audio bug was actually diagnosed, live test
numbers against real calls) live in a separate internal research repo,
`anielsud/tomoe-darwin` — a Python/pyobjc spike, not something this
package depends on or ports code from directly. Treat it as prior art
for *why* these mechanisms work, not as a second implementation to
keep in sync with this one.
