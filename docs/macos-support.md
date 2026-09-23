# macOS Support (in progress)

Status: two of the platform-specific packages macOS needs are built and
individually validated against a live Teams call. `cmd/tomoe` itself does
not build on macOS yet — see [Status](#status) for exactly what's missing.

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

Builds cleanly and runs standalone (`go build ./internal/teamsvideo
./internal/guestaudio`, plus their `cmd/video-signal-test` /
`cmd/guest-audio-test` diagnostics). **Not yet integrated** with anything
else — `cmd/tomoe` still doesn't build on macOS, because:

- `internal/hotkey`, `internal/clipboard`, `internal/notify` have no
  `_darwin.go` implementation.
- `internal/audio`'s **microphone** path (not the PulseAudio monitor-source
  path) is already portable `malgo` code with no Linux-specific calls in
  it — likely close to free once someone wires it up, but not done.
- Meeting-start detection has no macOS implementation at all yet (no
  equivalent to the PulseAudio dual-stream signal).
- The cluster-labeling step described above (the actual point of this
  work) hasn't been written.
- No macOS leg in CI (`.github/workflows/ci.yml` is `ubuntu-24.04`-only).

## Background

The video-ring/OCR mechanism, the ScreenCaptureKit gotchas, and the full
evidentiary trail (why AXUIElement was tried and disproven first, how the
planar-audio bug was actually diagnosed, live test numbers against real
calls) live in a separate internal research repo,
`anielsud/tomoe-darwin` — a Python/pyobjc spike, not something this
package depends on or ports code from directly. Treat it as prior art for
*why* these mechanisms work, not as a second implementation to keep in
sync with this one.
