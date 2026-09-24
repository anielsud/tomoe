# Tomoe PC — Project Instructions

## Project Overview

Local-first speech-to-text desktop application, built for Linux; macOS
support is in progress (see `docs/macos-support.md`), not yet build-ready.
Two modes of operation:

1. **CLI dictation** (`tomoe`) — global hotkey triggers mic capture, transcribes speech, pastes result into the focused window (terminal-aware: Ctrl+Shift+V for terminals, Ctrl+V otherwise)
2. **Meeting transcription GUI** (`tomoe-gui`) — Wails v2 desktop app with live scrolling transcript, mic + system audio capture, speaker identification, session management, export

- **License:** GPLv3
- **Language:** Go 1.22+
- **Target OS:** Ubuntu Linux 24.04+ (X11 primary, Wayland best-effort) — shipping today. macOS — in progress, not yet build-ready.
- **Audio:** PipeWire (with PulseAudio compat layer) via malgo/miniaudio
- **GPU:** NVIDIA CUDA via ONNX Runtime, automatic CPU fallback
- **GUI:** Wails v2 + React + TypeScript + Vite

## Architecture

### Inference Stack

```
Go binary → cgo → sherpa-onnx C API → ONNX Runtime (CUDA EP / CPU EP)
  → Parakeet TDT 0.6B v3 INT8 (encoder + decoder + joiner, 25 languages)
  → English streaming Zipformer INT8 (~70MB, live pass-1 via OnlineRecognizer)
  → Bengali Zipformer transducer (~87MB, streaming via OnlineRecognizer)
  → Silero VAD (~2MB)
  → 3D-Speaker embedding model (~25MB)
```

### Data Flow (Meeting Mode)

```
Mic Capturer → StreamCapturer → VAD → Transcribe(lang) → Segment{speaker:"You",lang:"en"}
                                                                          │
Monitor Capturer → StreamCapturer → VAD → Transcribe(lang) → Embed → Cluster → Segment{speaker:"Person N",lang:"bn"}
                                                                                │
                                                                          Wails EventsEmit
                                                                                │
                                                                         React Frontend
```

Language is selected manually via system tray sub-menus or GUI dropdown (no auto-detection).

### Data Flow (CLI Dictation)

```
Mic Capturer → StreamCapturer → VAD → Transcribe(lang) → Segment
                                                              │
                                                     Clipboard.Write(text)
                                                              │
                                                    Clipboard.AutoPaste()
                                                    (detects terminal vs GUI app)
```

### Key Design Decisions

- **X11 hotkey grabs**: Direct `XGrabKey` via cgo (not golang-design/hotkey). Grabs all combinations of NumLock/CapsLock/ScrollLock masks. Single dispatch loop per process, shared across all listeners.
- **Hotkey re-grab**: Audio device operations (malgo/PulseAudio) can interfere with X11 key grabs. `hotkey.ReGrabAll()` is called after every coordinator stop to restore grabs.
- **Terminal-aware paste**: `isTerminalFocused()` queries `WM_CLASS` via `xprop -id $(xdotool getactivewindow)` to detect terminal emulators and send the correct paste keystroke.
- **Signal handler fix**: ONNX Runtime / WebKit install SIGSEGV handlers without `SA_ONSTACK`, crashing Go goroutines on alternate signal stacks. `sigfix.AfterSherpa()` patches this on every frontend-bound method call.
- **Async session save**: `StopSession()` releases the mutex immediately, emits `session:stopped`, then runs MP3 encoding + session save in a background goroutine.
- **VAD activity channel**: Coordinator exposes an `Activity()` channel signaled when `vad.IsSpeech()` returns true, used to reset the silence timer during continuous speech (not just on completed segments).
- **PulseAudio meeting detection**: Simultaneous source-output (mic) + sink-input (speaker) from the same PID reliably indicates an active meeting. cgo bindings to libpulse (`#cgo pkg-config: libpulse`) follow the same pattern as `hotkey_linux.go`: static C globals, thread-locked event loop, `//export` callbacks. Platform identified via native app name or `xdotool` window title matching for browser-based meetings.
- **Manual language selection via EngineSet**: `EngineSet` holds a `map[string]Engine` (e.g., "en"→Parakeet, "bn"→Bengali Zipformer). Does NOT implement `Engine` — callers explicitly pick a language via `Get(lang)`. Tray sub-menus provide per-language start items; hotkey press uses the default language. Sessions store language code for re-transcription with a different engine.
- **Hotword boosting**: sherpa-onnx supports `modified_beam_search` with `HotwordsFile` for Parakeet TDT. Works independently of multilingual. Configurable via `[transcription]` section in config.toml.
- **macOS speaker naming (in progress)**: not a port of the Linux audio-only clustering approach — macOS has a second, independent naming signal Linux doesn't (Teams' visual active-speaker ring + name label, read via `internal/teamsvideo`). Plan is to keep `internal/speaker`'s embedding+clustering unchanged and *label* a cluster ID with a real name whenever a fresh video hint lands, carrying that label forward for the cluster's later turns — a cluster that never gets a hint still falls back to "Person N" exactly like Linux does today. See `docs/macos-support.md`.
- **Two-pass transcription**: Parakeet TDT is an *offline* recognizer even in "live" mode — a full non-streaming decode per completed VAD segment — so nothing appeared until a pause. `internal/transcribe.StreamingEngine` (English streaming Zipformer INT8, `sherpa.OnlineRecognizer`) is pass 1: fed the same VAD windows, polled continuously for a growing partial hypothesis, so text appears as it's spoken. On segment completion, that partial text is emitted immediately (`session.Segment.Status: "pending"`); the segment's audio is also queued to `live.Coordinator`'s `refineWorker`, which re-decodes it through Parakeet (pass 2, full-context, higher fidelity) and supersedes it via `SegmentUpdates()` (`Status: ""`). English-only and optional — nil `StreamingEngine` (model not downloaded, or a non-English session) falls back to the original single-pass behavior exactly.

## Project Structure

```
tomoe-pc/
├── cmd/
│   ├── tomoe/              # CLI entry point
│   └── tomoe-gui/          # Wails GUI entry point
├── internal/
│   ├── audio/              # Audio capture, streaming, monitor sources
│   ├── backend/            # Wails Go backend (app, events, tray, hotkey)
│   ├── clipboard/          # Clipboard write + terminal-aware auto-paste
│   ├── config/             # TOML config
│   ├── daemon/             # CLI daemon orchestration
│   ├── gpu/                # GPU detection, ONNX Runtime EP selection
│   ├── guestaudio/         # [macOS, in progress] ScreenCaptureKit window-audio tap (cgo + ObjC)
│   ├── hotkey/             # Global hotkey (X11 key grabs with lock-mask handling)
│   ├── langid/             # Spoken language identification (Whisper tiny INT8)
│   ├── live/               # Live transcription coordinator + per-source pipelines
│   ├── meeting/            # Automatic meeting detection via PulseAudio cgo bindings
│   ├── models/             # Model download and management
│   ├── notify/             # Desktop notifications (notify-send)
│   ├── platform/           # Services aggregation layer
│   ├── session/            # Session storage, export (MD/TXT/SRT), audio (M4A)
│   ├── sigfix/             # ONNX Runtime / WebKit signal handler fix
│   ├── speaker/            # Speaker embedding extraction + cosine clustering
│   ├── teamsvideo/         # [macOS, in progress] Teams window capture + active-speaker ring (cgo)
│   └── transcribe/         # sherpa-onnx / Parakeet TDT integration
├── frontend/               # React + TypeScript + Vite
│   └── src/
│       ├── components/     # TranscriptPane, SessionList, SourceSelector, etc.
│       ├── hooks/          # useTranscript, useSession
│       └── types.ts        # TypeScript types mirroring Go structs
├── docs/                   # Tech specs (speech-to-text-tech-brief.md)
├── wails.json
├── go.mod
└── Makefile
```

## Build & Development

```bash
make dev-deps         # Install Ubuntu system packages
make dev-tools        # Install Go tools (golangci-lint, goimports, wails)
make build            # Build CLI + GUI (if webkit2gtk available)
make build-gui        # Build GUI binary only
make test             # Run unit tests (stages frontend first)
make vet              # Run go vet (stages frontend first)
make lint             # Run golangci-lint (stages frontend first)
make dev-gui          # Wails dev mode with hot-reload
make download-model   # Download models (~685MB total — includes the English streaming Zipformer archive, which bundles unused fp32 weights alongside the int8 ones actually used)
make install          # Install to $GOPATH/bin
make dev-cert-mac     # One-time: create a stable local code-signing identity (macOS only)
make install-gui-mac  # Rebuild GUI, (re)install /Applications/Tomoe.app + Dock icon (macOS only)
make install-gpu      # Install CUDA toolkit + sherpa-onnx GPU libraries
```

### Build Requirements

- Go 1.22+, C/C++ toolchain (gcc/g++ for cgo)
- ONNX Runtime (>=1.17.0) + sherpa-onnx C API
- Node.js 18+ (for frontend)
- `libwebkit2gtk-4.1-dev`, `libappindicator3-dev`, `libgtk-3-dev` (for GUI)
- `libx11-dev`, `libpulse-dev`, `libasound-dev` (for audio/hotkey)
- `xdotool`, `xprop` (for auto-paste on X11)

### Important Build Note

`make vet`, `make lint`, and `make test` all depend on `stage-frontend`, which builds the React frontend and copies `dist/` into `cmd/tomoe-gui/frontend/` for `go:embed`. This is required because `cmd/tomoe-gui/main.go` embeds the frontend assets.

## Key Dependencies

| Package | Purpose | cgo |
|---|---|---|
| `k2-fsa/sherpa-onnx` | Transcription engine (ONNX Runtime + Parakeet TDT) | Yes |
| `gen2brain/malgo` | Audio capture (miniaudio bindings) | Yes |
| `wailsapp/wails/v2` | Desktop GUI framework | Yes |
| `fyne.io/systray` | System tray (AppIndicator3 on Linux) | Yes |
| `atotto/clipboard` | Clipboard write | No |
| `pelletier/go-toml` | Config file parsing (TOML) | No |
| `google/uuid` | Session IDs | No |
| `schollz/progressbar` | CLI progress bars | No |
| `libpulse` (C) | PulseAudio meeting detection (cgo via pkg-config) | Yes |

## Configuration

Config: `~/.config/tomoe/config.toml`
Models: `~/.local/share/tomoe/models/`
Sessions: `~/.local/share/tomoe/sessions/`

### Default Hotkeys

- `Super+Shift+S` — toggle dictation
- `Super+Shift+X` — toggle meeting recording

## CLI Commands

```
tomoe                     # Start daemon (auto-init on first run)
tomoe start               # Alias for above
tomoe init                # Manual system detection + config generation + model download
tomoe stop                # Stop daemon
tomoe status              # Show daemon/system/model/GPU info
tomoe transcribe <file>   # Transcribe audio file (WAV, FLAC, OGG)
tomoe model download      # Force re-download model
tomoe model status        # Show model info + integrity check
tomoe devices             # List audio input devices
tomoe config              # Print current config
```

## Coding Conventions

- Use `internal/` for all non-main packages — nothing is exported outside the module
- Platform-specific code uses `_linux.go`/`_darwin.go` filename-suffix build constraints (applies to `.go`, `.c`, and `.m` files alike — no explicit `//go:build` comment needed, though the macOS packages add one anyway for clarity). `go build ./...` is not safe to run unscoped on a single platform — it hard-fails the moment it reaches a same-OS-only package (e.g. `cmd/hotkey-test`, an X11-only diagnostic CLI, on macOS). Build/test specific package paths instead, same as the Makefile already does for `./cmd/tomoe`.
- Audio format: 16kHz mono PCM float32 (Parakeet TDT native input)
- Config format: TOML via `pelletier/go-toml`
- GUI build requires `-tags production,webkit2_41`
- Verbose logging in CLI daemon (hotkey dispatch, audio events) is intentional

## Tech Spec

The authoritative tech spec is at `docs/speech-to-text-tech-brief.md`.
For the in-progress macOS port specifically, see `docs/macos-support.md`.
