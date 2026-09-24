.PHONY: dev-deps dev-tools stage-frontend fmt lint vet test test-integration test-coverage build build-gui build-cuda package install dev-cert-mac install-gui-mac install-gpu clean download-model dev-gui

BINARY      := tomoe
GUI_BINARY  := tomoe-gui
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS     := -ldflags "-X main.Version=$(VERSION)"
GOFLAGS     := -v
GOBIN       := $(shell go env GOPATH)/bin
INSTALL_DIR := $(GOBIN)
LINT_VERSION := v2.11.3
SHERPA_VER   := v1.12.28
TOMOE_LIB    := $(HOME)/.local/share/tomoe/lib

# macOS .app bundle install (install-gui-mac target)
APP_NAME         := Tomoe
APP_BUNDLE       := build/darwin/$(APP_NAME).app
APPLICATIONS_DIR := /Applications
# A stable local identity, not ad-hoc (`codesign --sign -`): ad-hoc
# signatures have no consistent identity across rebuilds, and macOS
# TCC (Screen Recording/Microphone/etc. grants) can silently stop
# honoring a "Tomoe" entry the Settings UI still shows as enabled once
# the signature backing it changes -- exactly the failure mode hit
# testing this. Signing every build with the same self-signed identity
# (see dev-cert-mac) keeps grants valid across rebuilds.
CODESIGN_IDENTITY := Tomoe Dev Signing

UNAME := $(shell uname)

# Auto-detect webkit2gtk for GUI build (Linux only — macOS uses WKWebView
# natively via Wails, no webkit2gtk-equivalent package to detect).
HAS_WEBKIT := $(shell pkg-config --exists webkit2gtk-4.1 2>/dev/null && echo yes || echo no)

## Development setup ──────────────────────────────────────────────────

dev-deps: ## Install system packages needed for development (Ubuntu or macOS)
ifeq ($(UNAME),Darwin)
	@echo "macOS: hotkey (Carbon), clipboard/notify (osascript), and audio"
	@echo "(malgo/CoreAudio) all use frameworks already in Xcode's SDK --"
	@echo "no Homebrew packages needed for the CLI. Only Node is required:"
	@command -v node >/dev/null 2>&1 || brew install node
else
	sudo apt install -y build-essential pkg-config \
	  libx11-dev libxtst-dev libxkbcommon-dev \
	  libasound-dev portaudio19-dev libportaudio2 libpulse-dev pulseaudio-utils \
	  xclip xdotool wl-clipboard wtype libnotify-bin ffmpeg \
	  libwebkit2gtk-4.1-dev libappindicator3-dev libgtk-3-dev
endif
	@echo "Installing Node.js dependencies for frontend..."
	cd frontend && npm install

dev-tools: ## Install Go development tools (golangci-lint, goimports, wails)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/wailsapp/wails/v2/cmd/wails@latest

## Frontend staging (needed for go:embed in cmd/tomoe-gui) ────────────

stage-frontend: ## Build frontend and stage dist for go:embed
	@if [ -d frontend ] && command -v npm >/dev/null 2>&1; then \
		(cd frontend && npm install --silent && npm run build) && \
		mkdir -p cmd/tomoe-gui/frontend && \
		rm -rf cmd/tomoe-gui/frontend/dist && \
		cp -r frontend/dist cmd/tomoe-gui/frontend/dist; \
	elif [ ! -d cmd/tomoe-gui/frontend/dist ]; then \
		echo "Warning: frontend/dist not staged (npm not available). Go tools may fail on go:embed."; \
	fi

## Code quality ───────────────────────────────────────────────────────

fmt: ## Format Go source files
	gofmt -w .
	goimports -w .

lint: stage-frontend ## Run golangci-lint
	golangci-lint run ./...

vet: stage-frontend ## Run go vet
	go vet ./...

## Testing ────────────────────────────────────────────────────────────

test: stage-frontend ## Run unit tests
	go test $(GOFLAGS) ./...

test-integration: ## Run integration tests (requires model + hardware)
	go test $(GOFLAGS) -tags integration -timeout 120s ./...

test-coverage: ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## Build ──────────────────────────────────────────────────────────────

build: ## Build CLI binary (and GUI if webkit2gtk available, or always on macOS)
	CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/tomoe
ifeq ($(UNAME),Darwin)
	@echo "macOS: building frontend + GUI (WKWebView via Wails, no webkit2gtk needed)..."
	cd frontend && npm install --silent && npm run build
	mkdir -p cmd/tomoe-gui/frontend
	rm -rf cmd/tomoe-gui/frontend/dist
	cp -r frontend/dist cmd/tomoe-gui/frontend/dist
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build $(GOFLAGS) $(LDFLAGS) -tags production -o $(GUI_BINARY) ./cmd/tomoe-gui
else ifeq ($(HAS_WEBKIT),yes)
	@echo "webkit2gtk-4.1 detected, building frontend + GUI..."
	cd frontend && npm install --silent && npm run build
	mkdir -p cmd/tomoe-gui/frontend
	rm -rf cmd/tomoe-gui/frontend/dist
	cp -r frontend/dist cmd/tomoe-gui/frontend/dist
	CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -tags production,webkit2_41 -o $(GUI_BINARY) ./cmd/tomoe-gui
else
	@echo "webkit2gtk-4.1 not found, skipping GUI build."
endif

build-gui: build-frontend ## Build GUI binary only (webkit2gtk-4.1 on Linux, WKWebView on macOS)
	mkdir -p cmd/tomoe-gui/frontend
	rm -rf cmd/tomoe-gui/frontend/dist
	cp -r frontend/dist cmd/tomoe-gui/frontend/dist
ifeq ($(UNAME),Darwin)
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build $(GOFLAGS) $(LDFLAGS) -tags production -o $(GUI_BINARY) ./cmd/tomoe-gui
else
	CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -tags production,webkit2_41 -o $(GUI_BINARY) ./cmd/tomoe-gui
endif

build-frontend: ## Build React frontend
	cd frontend && npm install && npm run build

build-cuda: build ## Same binary — CUDA EP is selected at runtime via config

dev-gui: ## Run Wails dev mode with hot-reload
	cd frontend && npm install
	wails dev

## Distribution ───────────────────────────────────────────────────────

package: build ## Create release tarball
	mkdir -p dist
	tar czf dist/$(BINARY)-linux-amd64.tar.gz $(BINARY) $(wildcard $(GUI_BINARY)) README.md LICENSE docs/

## Install / Clean ────────────────────────────────────────────────────

install: build ## Install to GOPATH/bin
	install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
ifeq ($(HAS_WEBKIT),yes)
	install -m 755 $(GUI_BINARY) $(INSTALL_DIR)/$(GUI_BINARY)
endif

dev-cert-mac: ## One-time: create a stable local code-signing identity so Tomoe.app's TCC grants survive rebuilds (macOS only)
ifneq ($(UNAME),Darwin)
	$(error dev-cert-mac is macOS-only)
endif
	@if security find-certificate -c "$(CODESIGN_IDENTITY)" >/dev/null 2>&1; then \
		echo "'$(CODESIGN_IDENTITY)' already exists in the login keychain, skipping."; \
	else \
		echo "Creating local code-signing identity '$(CODESIGN_IDENTITY)'..."; \
		TMPD=$$(mktemp -d) && \
		openssl req -x509 -newkey rsa:2048 -keyout $$TMPD/key.pem -out $$TMPD/cert.pem -days 3650 -nodes \
		  -subj "/CN=$(CODESIGN_IDENTITY)" \
		  -addext "basicConstraints=critical,CA:true" \
		  -addext "keyUsage=critical,digitalSignature,keyCertSign" \
		  -addext "extendedKeyUsage=critical,codeSigning" && \
		security import $$TMPD/key.pem -k ~/Library/Keychains/login.keychain-db -A && \
		security import $$TMPD/cert.pem -k ~/Library/Keychains/login.keychain-db -A && \
		rm -rf $$TMPD; \
		echo "Created '$(CODESIGN_IDENTITY)'. If macOS ever prompts for keychain access when codesign uses it, choose \"Always Allow\"."; \
	fi

install-gui-mac: build-gui dev-cert-mac ## Rebuild the GUI and (re)install /Applications/Tomoe.app + Dock icon (macOS only)
ifneq ($(UNAME),Darwin)
	$(error install-gui-mac is macOS-only)
endif
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS
	mkdir -p $(APP_BUNDLE)/Contents/Resources
	cp $(GUI_BINARY) $(APP_BUNDLE)/Contents/MacOS/$(GUI_BINARY)
	sed 's/__VERSION__/$(VERSION)/g' packaging/macos/Info.plist.template > $(APP_BUNDLE)/Contents/Info.plist
	codesign --sign "$(CODESIGN_IDENTITY)" --force --deep $(APP_BUNDLE)
	rm -rf "$(APPLICATIONS_DIR)/$(APP_NAME).app"
	ditto $(APP_BUNDLE) "$(APPLICATIONS_DIR)/$(APP_NAME).app"
	@echo "Installed $(APPLICATIONS_DIR)/$(APP_NAME).app (version $(VERSION)), signed with '$(CODESIGN_IDENTITY)'."
	@echo "First install with this identity still needs Screen Recording/Microphone/Accessibility granted once in System Settings > Privacy & Security -- after that, rebuilds should keep working without re-granting."

install-gpu: ## Install CUDA toolkit + sherpa-onnx GPU libraries for NVIDIA acceleration
	@echo "=== Step 1: Installing CUDA 12 toolkit + cuDNN 9 ==="
	@if ! dpkg -l cuda-toolkit-12-8 >/dev/null 2>&1; then \
		echo "Adding NVIDIA CUDA repository..."; \
		wget -q https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb -O /tmp/cuda-keyring.deb; \
		sudo dpkg -i /tmp/cuda-keyring.deb; \
		rm -f /tmp/cuda-keyring.deb; \
		sudo apt-get update; \
		sudo apt-get install -y cuda-toolkit-12-8 libcudnn9-cuda-12; \
	else \
		echo "CUDA toolkit already installed, skipping."; \
	fi
	@echo "=== Step 2: Downloading sherpa-onnx GPU libraries ($(SHERPA_VER)) ==="
	mkdir -p $(TOMOE_LIB)
	@if [ ! -f "$(TOMOE_LIB)/libonnxruntime_providers_cuda.so" ]; then \
		echo "Downloading sherpa-onnx $(SHERPA_VER) CUDA 12 release..."; \
		curl -L "https://github.com/k2-fsa/sherpa-onnx/releases/download/$(SHERPA_VER)/sherpa-onnx-$(SHERPA_VER)-cuda-12.x-cudnn-9.x-linux-x64-gpu.tar.bz2" \
			| tar xjf - --strip-components=2 -C $(TOMOE_LIB) \
				"sherpa-onnx-$(SHERPA_VER)-cuda-12.x-cudnn-9.x-linux-x64-gpu/lib/libonnxruntime.so" \
				"sherpa-onnx-$(SHERPA_VER)-cuda-12.x-cudnn-9.x-linux-x64-gpu/lib/libonnxruntime_providers_cuda.so" \
				"sherpa-onnx-$(SHERPA_VER)-cuda-12.x-cudnn-9.x-linux-x64-gpu/lib/libonnxruntime_providers_shared.so" \
				"sherpa-onnx-$(SHERPA_VER)-cuda-12.x-cudnn-9.x-linux-x64-gpu/lib/libsherpa-onnx-c-api.so"; \
	else \
		echo "GPU libraries already present in $(TOMOE_LIB), skipping."; \
	fi
	@echo ""
	@echo "=== GPU setup complete ==="
	@echo "GPU libraries installed to: $(TOMOE_LIB)"
	@echo "Ensure gpu_enabled = true in ~/.config/tomoe/config.toml"
	@ls -lh $(TOMOE_LIB)/*.so

clean: ## Remove build artifacts
	rm -f $(BINARY) $(GUI_BINARY)
	rm -rf dist/ coverage.out coverage.html

## Model ──────────────────────────────────────────────────────────────

download-model: build ## Download Parakeet TDT INT8 model + Silero VAD + Speaker Embedding
	./$(BINARY) model download

## Help ───────────────────────────────────────────────────────────────

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
