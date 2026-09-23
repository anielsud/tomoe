# Tomoe PC — Local-First Speech-to-Text for Linux

**Repository:** `sosuke-ai/tomoe-pc`
**License:** GPLv3

## Project Overview

Tomoe PC is the desktop client component of the Tomoe ecosystem, which also includes a dedicated hardware recording device and a server-side backend. This client is a local-first speech-to-text application built in Go. It captures audio from the microphone or system audio, transcribes it using NVIDIA's Parakeet TDT 0.6B v2 model (FP16 quantization) via sherpa-onnx, and delivers the text to the user's clipboard or a UI. The primary goal is native Linux desktop integration with a single-binary distribution model. GPU acceleration (CUDA) is used when available via ONNX Runtime's CUDA execution provider, with automatic CPU fallback.

### Transcription Engine

**Model:** NVIDIA Parakeet TDT 0.6B v2 — a 600-million-parameter English-only ASR model using the FastConformer encoder with Token-and-Duration Transducer (TDT) decoder. It is the #1 ranked model on the Hugging Face Open ASR Leaderboard with 6.05% WER, significantly outperforming Whisper Large V3 Turbo (7.75% WER) while being smaller (0.6B vs 0.8B parameters) and dramatically faster.

**Quantization:** FP16 (half-precision) exclusively. FP16 leverages NVIDIA tensor cores for hardware-accelerated inference, uses ~1.2GB on disk (~1.8–2.5GB VRAM during inference), and has negligible accuracy loss compared to FP32. This is the best performance-to-accuracy tradeoff for GPU inference.

**Inference Runtime:** sherpa-onnx (by k2-fsa) — a C/C++ library wrapping ONNX Runtime with Go bindings. It provides a unified API for model loading, audio preprocessing, TDT decoding, and VAD (Silero). Pre-converted FP16 ONNX models are available for direct download from the sherpa-onnx releases.

**Language:** English only (current scope).

**Inference Stack:**
```
Tomoe Go binary
  → cgo → sherpa-onnx C API
    → ONNX Runtime (CUDA Execution Provider for GPU, CPU EP fallback)
      → Parakeet TDT 0.6B v2 FP16 (encoder.fp16.onnx + decoder.fp16.onnx + joiner.fp16.onnx)
    → Silero VAD (voice activity detection, ~2MB)
```

## Target Platform (Phase 1)

- **OS:** Ubuntu Linux 24.04+
- **Display Server:** X11 (primary), Wayland (best-effort)
- **Audio System:** PipeWire (with PulseAudio compatibility layer)
- **GPU:** NVIDIA CUDA (recommended for FP16 inference via ONNX Runtime CUDA EP; CPU fallback supported)

## Core Architecture

```
tomoe-pc/
├── cmd/
│   └── tomoe/                  # Main entry point
├── internal/
│   ├── audio/                # Audio capture and device management
│   │   ├── capture.go        # Interface definitions
│   │   └── capture_linux.go  # Linux implementation (PipeWire/PulseAudio)
│   ├── transcribe/           # sherpa-onnx / Parakeet TDT integration
│   │   ├── engine.go         # Transcription engine interface
│   │   └── parakeet.go       # sherpa-onnx Go bindings wrapper for Parakeet TDT
│   ├── clipboard/            # Clipboard write operations
│   │   └── clipboard_linux.go
│   ├── hotkey/               # Global hotkey registration
│   │   └── hotkey_linux.go
│   ├── gpu/                  # GPU detection and ONNX Runtime EP selection
│   │   └── detect.go
│   └── models/               # Model download and management
│       └── manager.go
├── frontend/                 # Wails v2 frontend (Phase 2)
│   ├── src/
│   └── ...
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

### Language & Framework

- **Language:** Go 1.22+
- **GUI Framework:** Wails v2 (Phase 2 — meeting transcription UI)
- **Build System:** Makefile with cgo support for sherpa-onnx C API linkage

## Phase 1 — Dictation Mode (CLI)

### Goal

A single binary (`tomoe`) that requires zero manual configuration. On first run, it auto-detects the system (GPU, audio, display server), generates a config file with sensible defaults, downloads the model, and starts a background daemon. The user presses a global hotkey to start/stop recording from the microphone. On stop, audio is transcribed locally and the resulting text is copied to the clipboard and optionally auto-pasted into the active window.

### Functional Requirements

1. **Global Hotkey**
   - Default binding: `Super+Shift+R` (configurable)
   - Toggle behavior: first press starts recording, second press stops and triggers transcription
   - Visual/audio feedback: play a short system sound or show a desktop notification on start/stop via `notify-send`
   - On X11, use `XGrabKey` via cgo or a library like `golang-design/hotkey`
   - On Wayland, fall back to D-Bus global shortcuts portal (`org.freedesktop.portal.GlobalShortcuts`)

2. **Microphone Audio Capture**
   - Use **gen2brain/malgo** (Go bindings for miniaudio) for microphone capture
   - Capture format: 16kHz mono PCM float32 (Parakeet TDT native input format)
   - Buffer audio in memory (no intermediate file writes for short dictations)
   - Support device selection via config (default to system default input device)
   - Handle device hot-plug gracefully (reconnect if device is lost)

3. **Local Transcription via Parakeet TDT (sherpa-onnx)**
   - Use **k2-fsa/sherpa-onnx** with its Go bindings for inference
   - Model: NVIDIA Parakeet TDT 0.6B v2, FP16 quantization exclusively
   - Model files: `encoder.fp16.onnx` (~600MB) + `decoder.fp16.onnx` (~7MB) + `joiner.fp16.onnx` (~1.7MB) + `tokens.txt`
   - VAD: Silero VAD (`silero_vad.onnx`, ~2MB) for voice activity detection — segments speech from silence automatically
   - GPU backend: ONNX Runtime CUDA Execution Provider (auto-detected at runtime)
   - CPU fallback: ONNX Runtime CPU Execution Provider (no user configuration needed; automatic if CUDA is unavailable)
   - VRAM requirement: ~1.8–2.5GB for FP16 inference (any NVIDIA GPU with ≥4GB VRAM is comfortable)
   - Language: English only (hardcoded for current scope)

4. **Clipboard Integration**
   - Use **atotto/clipboard** for clipboard write
   - After transcription completes, copy text to system clipboard
   - Optional auto-paste: simulate `Ctrl+V` via `xdotool` on X11
   - On Wayland, use `wl-copy` for clipboard and `wtype` for simulated paste

5. **Model Management**
   - Model download is triggered automatically during auto-init if models are not present — no manual step required for first-time users
   - Downloads FP16 model archive from sherpa-onnx GitHub releases (`sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16.tar.bz2`, ~610MB) and Silero VAD (`silero_vad.onnx`, ~2MB)
   - Store models in `~/.local/share/tomoe/models/`
   - CLI commands available for manual management: `tomoe model download` (force re-download), `tomoe model status`
   - Show download progress with a terminal progress bar
   - Verify model integrity via checksum after download

6. **Configuration & Auto-Initialization**

   On first run (or if no config file exists), Tomoe performs automatic system detection and initialization:

   **Auto-Init Sequence:**
   1. **Detect GPU:** Query for NVIDIA GPU via `nvidia-smi` or by attempting to load ONNX Runtime CUDA EP. Record GPU name, VRAM, and CUDA availability.
   2. **Detect audio system:** Check for PipeWire (`pw-cli`), fall back to PulseAudio (`pactl`). Enumerate available input devices and select the system default.
   3. **Detect display server:** Check `$XDG_SESSION_TYPE` for `x11` or `wayland`. Set clipboard and hotkey backend accordingly.
   4. **Generate default config:** Write `~/.config/tomoe/config.toml` with detected values and sensible defaults.
   5. **Download model:** If `~/.local/share/tomoe/models/` is empty or incomplete, automatically download the Parakeet TDT FP16 model archive and Silero VAD. Show a progress bar during download.
   6. **Verify model integrity:** Check downloaded files against known checksums. Re-download if corrupt.
   7. **Print summary:** Display a one-time initialization summary showing detected system info, config path, and model status.

   **Default Config Values (`~/.config/tomoe/config.toml`):**
   ```toml
   # Generated by tomoe auto-init on <timestamp>

   [hotkey]
   binding = "Super+Shift+R"

   [audio]
   device = "default"              # Auto-detected system default input device

   [transcription]
   gpu_enabled = true              # Set to false if no NVIDIA GPU was detected
   model_path = "~/.local/share/tomoe/models/"

   [output]
   auto_paste = true               # Automatically paste transcribed text into active window
   clipboard = true
   ```

   - Config file location: `~/.config/tomoe/config.toml`
   - Model storage location: `~/.local/share/tomoe/models/`
   - If the user manually deletes the config, re-running `tomoe` triggers auto-init again
   - All auto-detected values can be overridden by the user at any time
   - `gpu_enabled` defaults to `true` only if an NVIDIA GPU with ≥4GB VRAM is detected; otherwise defaults to `false`

### CLI Interface

```
tomoe                     # Start daemon (runs auto-init if first run, then listens for hotkey)
tomoe start               # Alias for above
tomoe init                # Manually trigger system detection, config generation, and model download
tomoe stop                # Stop the daemon
tomoe status              # Show daemon status, system info, model info, GPU detection
tomoe transcribe <file>   # Transcribe an audio file directly (WAV, FLAC, OGG)
tomoe model download      # Force re-download of Parakeet TDT FP16 model + Silero VAD
tomoe model status        # Show downloaded model info, path, and integrity check
tomoe devices             # List available audio input devices
tomoe config              # Print current configuration
```

### Non-Functional Requirements

- Transcription latency for a 30-second clip: <1s on GPU (Parakeet TDT processes audio ~3000x faster than real-time on GPU), <5s on CPU
- Memory usage while idle (daemon waiting for hotkey): <50MB RSS
- Binary size: <30MB (excluding model files and ONNX Runtime shared library)
- Model download size: ~610MB (FP16 archive) + ~2MB (Silero VAD)
- VRAM usage during inference: ~1.8–2.5GB (FP16 on CUDA)
- Runtime dependency: ONNX Runtime shared library (`libonnxruntime.so`) — bundled with the release or installable via package manager

## Phase 2 — Meeting Transcription Mode (Wails GUI)

### Goal

Add a desktop GUI (via Wails v2) that allows the user to capture and transcribe both incoming and outgoing system audio in real-time. This is for transcribing meetings (Zoom, Google Meet, Teams) locally.

### Functional Requirements

1. **System Audio Capture (Loopback)**
   - Capture the system's audio output (what the user hears) using PipeWire's loopback/monitor source
   - On PipeWire: use the `.monitor` port of the default output sink
   - Capture microphone simultaneously for the user's own speech
   - Mix or keep separate as two channels for speaker diarization

2. **VAD-Segmented Transcription**
   - Use Silero VAD to detect speech segments in real-time from the audio stream
   - Each completed speech segment (utterance) is transcribed offline via Parakeet TDT
   - Display transcription results per utterance in the UI as they complete
   - Maintain a scrolling transcript view with timestamps
   - Note: This is pseudo-streaming (utterance-level, not word-level) — acceptable for meeting transcript generation

3. **Speaker Diarization (Stretch)**
   - If mic and system audio are on separate channels, label segments as "You" vs "Other"
   - For single-channel input, evaluate NVIDIA Sortformer (available via parakeet.cpp, supports up to 4 speakers) or defer to a future phase

4. **Wails v2 Frontend**
   - Framework: Wails v2 with a Svelte or React frontend (developer's choice based on Wails template availability)
   - UI elements:
     - **Source selector:** dropdown to pick audio sources (mic, system audio, both)
     - **Start/Stop button:** begins or ends a transcription session
     - **Live transcript pane:** scrollable, timestamped, auto-scrolling
     - **Session controls:** save transcript to file (Markdown, plain text, or SRT), copy to clipboard
     - **Settings panel:** GPU toggle, audio device config
   - System tray icon with start/stop controls and status indicator
   - Desktop notifications for session start/stop

5. **Session Management**
   - Save transcription sessions to `~/.local/share/tomoe/sessions/` as structured JSON
   - Each session includes: start time, duration, transcript segments with timestamps, model used, audio source info
   - Allow exporting to Markdown, plain text, or SRT subtitle format

### Wails Integration Notes

- Use Wails v2 bindings to expose Go backend functions to the frontend
- Audio capture and transcription run in Go goroutines; results are pushed to the frontend via Wails events
- The CLI daemon (Phase 1) and the GUI (Phase 2) should share the same `internal/` packages — the GUI is an additional entry point, not a rewrite

## Build & Distribution

### Build Requirements

- Go 1.22+
- C/C++ toolchain (gcc/g++ for cgo linkage to sherpa-onnx)
- ONNX Runtime shared library (≥1.17.0) — pre-built binaries available from Microsoft, CUDA variant for GPU support
- sherpa-onnx C API headers and library (built from source or pre-built release)

### Build Targets (Makefile)

```makefile
make build            # Build CLI binary for current platform (CPU)
make build-cuda       # Build with ONNX Runtime CUDA EP support
make build-gui        # Build Wails GUI binary
make install          # Install to /usr/local/bin
make test             # Run unit and integration tests
make download-model   # Download Parakeet TDT FP16 model + Silero VAD
make release          # Cross-compile release binaries (CI use)
```

### Distribution Strategy (Phase 1: Linux Only)

- **GitHub Releases:** pre-built `.deb` and `.tar.gz` binaries
- **Homebrew (Linuxbrew):** tap for `brew install tomoe`
- **Snap or AppImage:** for broad Ubuntu/distro compatibility (evaluate)
- Two build variants per release: `tomoe-linux-amd64` (CPU, bundles ONNX Runtime CPU) and `tomoe-linux-amd64-cuda` (bundles ONNX Runtime CUDA EP)
- Model files downloaded separately on first run (~612MB total)

## Future Roadmap (Post Phase 2)

- **AssemblyAI API fallback:** Add AssemblyAI Universal-2 as a cloud transcription backend for users without a capable GPU. Configurable via `transcription_backend: local | assemblyai` in config. AssemblyAI offers ~6.68% WER with real-time streaming via WebSocket at ~300ms latency and $0.15/hour pricing. Requires API key in config.
- **macOS support (in progress):** see [`macos-support.md`](macos-support.md) — not a straight CoreAudio/CoreML port; adds a video-based active-speaker signal (Teams' on-screen speaking indicator) that labels `internal/speaker`'s existing audio clusters with real names, since Linux's PulseAudio-based meeting detection has no macOS equivalent to build the same clustering-only approach on
- **Windows support:** DirectSound/WASAPI capture, Win32 hotkey API, ONNX Runtime CUDA/DirectML EP
- **Multilingual support:** Swap to Parakeet TDT 0.6B v3 (25 European languages with auto language detection, same architecture, same sherpa-onnx integration)
- **MCP server mode:** expose transcription as a tool via Model Context Protocol for integration with AI agents
- **Live translation:** chain transcription with a local translation model
- **Speaker diarization:** integrate NVIDIA Sortformer for multi-speaker identification

## Key Dependencies

| Dependency | Purpose | cgo Required |
|---|---|---|
| `k2-fsa/sherpa-onnx` (Go bindings) | Transcription engine (wraps ONNX Runtime + Parakeet TDT) | Yes |
| ONNX Runtime (`libonnxruntime.so`) | Model inference runtime (CUDA EP + CPU EP) | Linked via sherpa-onnx |
| Silero VAD (`silero_vad.onnx`) | Voice activity detection | No (loaded by sherpa-onnx) |
| `gen2brain/malgo` | Audio capture (miniaudio) | Yes |
| `golang-design/hotkey` | Global hotkey registration | Yes (X11) |
| `atotto/clipboard` | Clipboard write | No |
| `wailsapp/wails/v2` | Desktop GUI framework (Phase 2) | Yes |
| `pelletier/go-toml` | Config file parsing | No |
| `schollz/progressbar` | CLI progress bars (model download) | No |

## Success Criteria

### Phase 1
- User can install a single binary, run `tomoe`, and everything auto-initializes: system is detected, config is generated, model is downloaded, GPU is configured — zero manual setup
- After auto-init, user presses a hotkey, speaks, and has transcribed text appear in their clipboard within seconds
- GPU is automatically used when available via ONNX Runtime CUDA EP with no user configuration
- Subsequent launches skip init and start the daemon immediately
- Transcription achieves ≤6.05% WER on English speech (matching Parakeet TDT v2 benchmark)

### Phase 2
- User can open the GUI, select audio sources, and get a live scrolling transcript of a meeting
- Transcripts can be saved and exported in multiple formats
- The GUI and CLI coexist — the CLI daemon can run alongside or independently of the GUI
