# Tomoe PC

The Linux desktop client for the [Tomoe](https://github.com/sosuke-ai/tomoe) system — a local-first speech-to-text suite. Captures microphone and system audio, transcribes locally using NVIDIA Parakeet TDT 0.6B v3 (INT8, 25 languages) via sherpa-onnx, and delivers text to clipboard or a live GUI transcript with speaker identification.

## Features

- **CLI dictation mode** — global hotkey triggers mic capture, transcribes speech, pastes result into the focused window (detects terminals for Ctrl+Shift+V)
- **Meeting transcription GUI** — Wails v2 desktop app with live scrolling transcript, mic + system audio capture, speaker identification, session management
- **Speaker identification** — mic audio labeled "You", system audio speakers clustered via 3D-Speaker embeddings ("Person 1", "Person 2", etc.)
- **Automatic meeting detection** — PulseAudio monitoring detects when a meeting app uses both mic and speaker, auto-starts/stops recording with platform identification (Teams, Meet, Zoom, Webex, Slack)
- **Session management** — save, load, export (Markdown, plain text, SRT), delete sessions with recorded audio (M4A), editable title and platform metadata
- **GPU acceleration** — NVIDIA CUDA via ONNX Runtime with automatic CPU fallback
- **25+ languages** — Parakeet TDT v3 INT8 (25 European languages) + Bengali via Zipformer, with manual language selection via tray sub-menus, GUI dropdown, or session re-transcription
- **Hotword boosting** — configurable hotwords file to fix misrecognitions (e.g., "claude", "haiku") via modified beam search
- **Session re-transcription** — re-process any saved session's audio to re-identify speakers via diarization
- **System tray** — background operation with AppIndicator3 tray icon

## Quick Start

```bash
# Install system dependencies (Ubuntu 24.04+)
make dev-deps

# Install Go tools (golangci-lint, wails)
make dev-tools

# Build CLI + GUI
make build

# First run — auto-detects system, creates config, downloads models (~690MB)
./tomoe

# Or launch the GUI
./tomoe-gui
```

## Build Requirements

- Go 1.22+
- C/C++ toolchain (gcc/g++ for cgo)
- ONNX Runtime shared library (>=1.17.0) + sherpa-onnx C API
- Node.js 18+ (for frontend)
- `libwebkit2gtk-4.1-dev` (for GUI)
- `libpulse-dev` (for PulseAudio meeting detection via cgo)
- `xdotool`, `xprop` (for clipboard auto-paste and meeting platform identification on X11)

## Make Targets

```bash
make build            # Build CLI + GUI (if webkit2gtk available)
make build-gui        # Build GUI binary only
make build-cuda       # Build with CUDA support
make test             # Run unit tests
make vet              # Run go vet
make lint             # Run golangci-lint
make dev-gui          # Wails dev mode with hot-reload
make download-model   # Download Parakeet TDT v3 INT8 + Silero VAD + Speaker Embedding
make install          # Install to $GOPATH/bin
make install-gpu      # Install CUDA toolkit + sherpa-onnx GPU libraries
make clean            # Remove build artifacts
```

## CLI Commands

```
tomoe                                 # Start daemon (auto-init on first run)
tomoe start                           # Alias for above
tomoe init                            # Manual system detection + config generation + model download
tomoe stop                            # Stop daemon
tomoe status                          # Show daemon/system/model/GPU info
tomoe version                         # Print version
tomoe transcribe <file>               # Transcribe audio file (WAV, FLAC, OGG, MP3, M4A)
tomoe model download                  # Force re-download base models
tomoe model download --multilingual   # Download base + Bengali models
tomoe model status                    # Show model info + integrity check
tomoe session list                    # List all saved sessions
tomoe session re-transcribe <id>      # Re-process a session's audio (re-identify speakers)
tomoe session replay <id>             # Compare the default pipeline with your config on a session's audio
tomoe tune <id> --ref <transcript>    # Find the best speaker-naming settings from a session recorded for tuning
tomoe devices                         # List audio input devices
tomoe config                          # Print current config
```

## Default Hotkeys

| Hotkey | Action |
|--------|--------|
| `Super+Shift+S` | Toggle dictation (CLI + GUI) |
| `Super+Shift+X` | Toggle meeting recording (CLI + GUI) |

Configurable in `~/.config/tomoe/config.toml`.

## Configuration

```toml
# ~/.config/tomoe/config.toml

[hotkey]
binding = 'Super+Shift+S'
meeting_binding = 'Super+Shift+X'

[audio]
device = 'default'

[transcription]
gpu_enabled = true
model_path = '~/.local/share/tomoe/models'
decoding_method = 'greedy_search'  # or 'modified_beam_search' for hotwords
hotwords_file = ''                 # path to hotwords.txt (one word/phrase per line)
hotwords_score = 1.5               # boost score for hotwords
max_active_paths = 4               # beam width for modified_beam_search
two_pass = false                   # English meetings: live streaming text, refined per utterance (experimental)

[multilingual]
enabled = false
languages = ['en']        # add 'bn' for Bengali
default_lang = 'en'       # language used when a hotkey fires without explicit language

[output]
auto_paste = true
clipboard = true
silence_timeout = 5.0

[meeting]
default_sources = 'both'
monitor_device = ''       # Linux: '' = default monitor source; macOS: '' = 'auto' (the meeting app once it makes sound, else everything), 'everything', or an app; 'none' = mic only
speaker_threshold = 0.65           # cosine similarity for a confident speaker match
max_speech_duration = 30.0         # longest utterance (s) before it's cut; each utterance gets one speaker label
min_silence_duration = 0.5         # pause (s) that ends an utterance
auto_save = true
auto_detect = true
sticky_grace_window = 3.0          # seconds a near-miss can still join the last speaker
sticky_threshold_margin = 0        # how near a near-miss must be; 0 = sticky rule off (experimental)
min_assign_duration = 0            # segments shorter than this (s) join the last speaker; 0 = off (experimental)
short_segment_grace_window = 15.0  # seconds after the last speaker that rule applies
split_on_speaker_change = false    # after a meeting, split lines where the speaker changes mid-line (experimental)
speaker_model = 'auto'             # voice model: 'auto' (English-trained for English, base otherwise), 'eres2net-en', 'eres2net-base'
diarize_during_meeting = true      # work out who spoke when while recording; false = the previous pass after the meeting
diarize_stride = 2                 # with it on: fingerprint every Nth analysis window; sets CPU use during the call (higher = less)
diarize_recluster = 10             # with it on: seconds between label updates
video_hint_window = ''             # macOS: window to read speakers from: '' = Teams meeting, 'none' = off, or an app's name (frames kept for a future rule)
video_hint_learn_interval = 0.35   # macOS: seconds between looks at the meeting window while a speaker needs naming
video_hint_check_interval = 1.0    # macOS: seconds between looks once everyone speaking has a name
record_for_tuning = false          # record meetings at full detail for `tomoe tune` (more CPU, ~250 MB/hour)
```

On macOS the app logs to `~/Library/Logs/Tomoe/tomoe-gui.log` (crash traces
included; kept to about 20 MB).

Settings missing from your `config.toml` take the defaults above. The `[meeting]`
speaker settings are reloaded while Tomoe runs; no restart needed. Everything can
also be changed from the GUI's Settings page.

### Trying the experimental pipeline

Two-pass transcription and two speaker-clustering rules (sticky speaker, short
segments) came out of the macOS port. They're off by default, so the pipeline
behaves as it always has, until they're proven on real recordings. To try them:

```toml
[transcription]
two_pass = true          # also downloads the English streaming model (~310MB)

[meeting]
speaker_threshold = 0.55
sticky_threshold_margin = 0.15
min_assign_duration = 0.7
split_on_speaker_change = true
```

To see what they change on your own recordings, replay a saved session. It runs
the audio through the pipeline once with the defaults and once with your current
config, then writes both transcripts for diffing:

```bash
tomoe session replay <session-id>
```

## Architecture

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
│   ├── gpu/                # GPU detection
│   ├── guestaudio/         # [macOS, in progress] ScreenCaptureKit window-audio tap
│   ├── hotkey/             # Global hotkey (X11 key grabs)
│   ├── langid/             # Spoken language identification (Whisper tiny, optional)
│   ├── live/               # Live transcription coordinator + per-source pipelines
│   ├── meeting/            # Automatic meeting detection (PulseAudio cgo)
│   ├── models/             # Model download and management
│   ├── notify/             # Desktop notifications
│   ├── platform/           # Services aggregation layer
│   ├── session/            # Session storage, export, audio recording
│   ├── sigfix/             # ONNX Runtime signal handler fix
│   ├── speaker/            # Speaker embedding + clustering
│   ├── teamsvideo/         # [macOS, in progress] Teams window capture + active-speaker ring
│   └── transcribe/         # sherpa-onnx / Parakeet TDT integration
├── frontend/               # React + TypeScript + Vite
└── Makefile
```

### Inference Stack

```
Go binary → cgo → sherpa-onnx C API → ONNX Runtime (CUDA EP / CPU EP)
  → Parakeet TDT 0.6B v3 INT8 (encoder + decoder + joiner, 25 languages)
  → Bengali Zipformer transducer (~87MB, streaming via OnlineRecognizer)
  → Silero VAD (~2MB)
  → 3D-Speaker embedding model (~25MB)
```

Language is selected manually (tray sub-menus, GUI dropdown, or per-session at re-transcribe time); no automatic language detection.

### Data Flow (Meeting Mode)

```
PulseAudio subscribe ──→ source-output + sink-input from same PID? ──→ MeetingStarted
                                                                              │
Mic Capturer → StreamCapturer → VAD → Transcribe(lang) → Segment{speaker:"You",lang:"en"}
                                                                          │
Monitor Capturer → StreamCapturer → VAD → Transcribe(lang) → Embed → Cluster → Segment{speaker:"Person N",lang:"bn"}
                                                                                │
                                                                         Wails EventsEmit
                                                                                │
                                                                         React Frontend
```

## Data Paths

| Path | Purpose |
|------|---------|
| `~/.config/tomoe/config.toml` | Configuration |
| `~/.local/share/tomoe/models/` | ONNX models |
| `~/.local/share/tomoe/sessions/` | Saved sessions (JSON + M4A) |
| `~/.local/share/tomoe/lib/` | GPU libraries (if installed) |

## Session Backup

A ready-made rsync/SSH backup script for the sessions directory lives in
[`scripts/backup/`](./scripts/backup/). Runs on cron or a systemd timer,
mirrors transcripts and audio to a remote host, and — after verifying each
remote copy — prunes local audio older than a configurable retention window.
Transcript `session.json` files are kept locally forever. See
[`scripts/backup/README.md`](./scripts/backup/README.md) for setup.

## Roadmap

- **macOS support (in progress)** — not a straight port: adds a video-based
  active-speaker signal (Teams' on-screen speaking indicator, OCR'd) that
  Linux has no equivalent of, used to label `internal/speaker`'s existing
  clusters with real names instead of "Person N". Window capture and a
  ScreenCaptureKit audio tap are built and individually validated against a
  live call; not yet integrated into `cmd/tomoe`. See
  [`docs/macos-support.md`](docs/macos-support.md).
- **Windows support** — not started.
- **Post-meeting delivery hooks** — on meeting completion, push the transcript (and optionally the recording) to an external destination: pipe into a user-defined CLI command, POST to a configurable web endpoint, or both

## License

GPLv3 — see [LICENSE](LICENSE).
