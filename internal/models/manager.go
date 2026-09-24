package models

import (
	"archive/tar"
	"compress/bzip2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/schollz/progressbar/v3"
)

// Status describes the state of downloaded models.
type Status struct {
	ParakeetReady            bool
	VADReady                 bool
	SpeakerEmbeddingReady    bool
	SpeakerSegmentationReady bool
	ParakeetPartial          bool // some but not all Parakeet files present
	ModelDir                 string
	EncoderPath              string
	DecoderPath              string
	JoinerPath               string
	TokensPath               string
	VADPath                  string
	SpeakerEmbeddingPath     string
	SpeakerSegmentationPath  string

	// Language identification (Whisper tiny)
	LangIDReady       bool
	LangIDEncoderPath string
	LangIDDecoderPath string

	// Bengali Zipformer transducer (streaming)
	BengaliReady       bool
	BengaliEncoderPath string
	BengaliDecoderPath string
	BengaliJoinerPath  string
	BengaliTokensPath  string

	// English streaming Zipformer transducer (realtime "pass 1" — see
	// internal/live's two-pass pipeline). Not required for Ready(): its
	// absence just means the live pipeline falls back to VAD+Parakeet
	// only, same as before this existed.
	EnglishStreamingReady       bool
	EnglishStreamingEncoderPath string
	EnglishStreamingDecoderPath string
	EnglishStreamingJoinerPath  string
	EnglishStreamingTokensPath  string
}

// Manager handles model download, extraction, and verification.
type Manager struct {
	modelDir string
}

// NewManager creates a Manager for the given model directory.
func NewManager(modelDir string) *Manager {
	return &Manager{modelDir: modelDir}
}

// ModelDir returns the model storage directory.
func (m *Manager) ModelDir() string {
	return m.modelDir
}

// Check inspects the model directory and reports what's present.
func (m *Manager) Check() *Status {
	parakeetDir := filepath.Join(m.modelDir, ParakeetSubdir)
	whisperDir := filepath.Join(m.modelDir, WhisperTinySubdir)
	bengaliDir := filepath.Join(m.modelDir, BengaliSubdir)
	englishStreamingDir := filepath.Join(m.modelDir, EnglishStreamingSubdir)

	s := &Status{
		ModelDir:                    m.modelDir,
		EncoderPath:                 filepath.Join(parakeetDir, encoderFile),
		DecoderPath:                 filepath.Join(parakeetDir, decoderFile),
		JoinerPath:                  filepath.Join(parakeetDir, joinerFile),
		TokensPath:                  filepath.Join(parakeetDir, tokensFile),
		VADPath:                     filepath.Join(m.modelDir, SileroVADFile),
		SpeakerEmbeddingPath:        filepath.Join(m.modelDir, SpeakerEmbeddingFile),
		SpeakerSegmentationPath:     filepath.Join(m.modelDir, PyannoteSegmentationSubdir, PyannoteSegmentationFile),
		LangIDEncoderPath:           filepath.Join(whisperDir, WhisperTinyEncoderFile),
		LangIDDecoderPath:           filepath.Join(whisperDir, WhisperTinyDecoderFile),
		BengaliEncoderPath:          filepath.Join(bengaliDir, bengaliEncoderFile),
		BengaliDecoderPath:          filepath.Join(bengaliDir, bengaliDecoderFile),
		BengaliJoinerPath:           filepath.Join(bengaliDir, bengaliJoinerFile),
		BengaliTokensPath:           filepath.Join(bengaliDir, bengaliTokensFile),
		EnglishStreamingEncoderPath: filepath.Join(englishStreamingDir, englishStreamingEncoderFile),
		EnglishStreamingDecoderPath: filepath.Join(englishStreamingDir, englishStreamingDecoderFile),
		EnglishStreamingJoinerPath:  filepath.Join(englishStreamingDir, englishStreamingJoinerFile),
		EnglishStreamingTokensPath:  filepath.Join(englishStreamingDir, englishStreamingTokensFile),
	}

	files := []string{s.EncoderPath, s.DecoderPath, s.JoinerPath, s.TokensPath}
	s.ParakeetReady = allFilesExist(files...)
	s.VADReady = fileExists(s.VADPath)
	s.SpeakerEmbeddingReady = fileExists(s.SpeakerEmbeddingPath)
	s.SpeakerSegmentationReady = fileExists(s.SpeakerSegmentationPath)
	s.LangIDReady = allFilesExist(s.LangIDEncoderPath, s.LangIDDecoderPath)
	s.BengaliReady = allFilesExist(s.BengaliEncoderPath, s.BengaliDecoderPath, s.BengaliJoinerPath, s.BengaliTokensPath)
	s.EnglishStreamingReady = allFilesExist(s.EnglishStreamingEncoderPath, s.EnglishStreamingDecoderPath, s.EnglishStreamingJoinerPath, s.EnglishStreamingTokensPath)

	// Detect partial download (some files exist but not all)
	if !s.ParakeetReady {
		count := 0
		for _, f := range files {
			if fileExists(f) {
				count++
			}
		}
		s.ParakeetPartial = count > 0
	}

	return s
}

// Ready reports whether all required models are present.
func (s *Status) Ready() bool {
	return s.ParakeetReady && s.VADReady
}

// DiarizationReady reports whether speaker diarization models are present.
func (s *Status) DiarizationReady() bool {
	return s.SpeakerEmbeddingReady && s.SpeakerSegmentationReady
}

// MultilingualReady reports whether Bengali model is present.
// Lang-id (Whisper tiny) is no longer required — language selection is manual.
func (s *Status) MultilingualReady() bool {
	return s.BengaliReady
}

// String returns a human-readable summary.
func (s *Status) String() string {
	parakeet := "not downloaded"
	if s.ParakeetReady {
		parakeet = "ready"
	} else if s.ParakeetPartial {
		parakeet = "incomplete (run 'tomoe model download --force')"
	}
	vad := "not downloaded"
	if s.VADReady {
		vad = "ready"
	}
	speaker := "not downloaded"
	if s.SpeakerEmbeddingReady {
		speaker = "ready"
	}
	segmentation := "not downloaded"
	if s.SpeakerSegmentationReady {
		segmentation = "ready"
	}
	diarization := "not ready"
	if s.DiarizationReady() {
		diarization = "ready"
	}
	langID := "not downloaded"
	if s.LangIDReady {
		langID = "ready"
	}
	bengali := "not downloaded"
	if s.BengaliReady {
		bengali = "ready"
	}
	multilingual := "not ready"
	if s.MultilingualReady() {
		multilingual = "ready"
	}
	englishStreaming := "not downloaded"
	if s.EnglishStreamingReady {
		englishStreaming = "ready"
	}
	return fmt.Sprintf("Model dir: %s\nParakeet TDT INT8: %s\nSilero VAD: %s\nSpeaker Embedding: %s\nSpeaker Segmentation: %s\nDiarization: %s\nLang-ID (Whisper tiny): %s\nBengali Zipformer: %s\nMultilingual: %s\nEnglish Streaming Zipformer (realtime pass): %s",
		s.ModelDir, parakeet, vad, speaker, segmentation, diarization, langID, bengali, multilingual, englishStreaming)
}

// Download downloads and extracts all required models.
// If force is true, existing models are re-downloaded.
func (m *Manager) Download(force bool) error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}

	status := m.Check()

	// Download Parakeet TDT archive
	if force || !status.ParakeetReady {
		if status.ParakeetPartial {
			fmt.Println("Incomplete Parakeet model detected, re-downloading...")
			// Clean up partial extraction
			parakeetDir := filepath.Join(m.modelDir, ParakeetSubdir)
			_ = os.RemoveAll(parakeetDir)
		}
		fmt.Println("Downloading Parakeet TDT 0.6B v3 INT8 model...")
		if err := m.downloadAndExtractArchive(ParakeetArchiveURL); err != nil {
			return fmt.Errorf("downloading Parakeet model: %w", err)
		}
		fmt.Println("Parakeet TDT model downloaded and extracted.")
	} else {
		fmt.Println("Parakeet TDT model already present, skipping.")
	}

	// Download Silero VAD
	if force || !status.VADReady {
		fmt.Println("Downloading Silero VAD model...")
		vadPath := filepath.Join(m.modelDir, SileroVADFile)
		if err := downloadFile(SileroVADURL, vadPath); err != nil {
			return fmt.Errorf("downloading Silero VAD: %w", err)
		}
		fmt.Println("Silero VAD model downloaded.")
	} else {
		fmt.Println("Silero VAD model already present, skipping.")
	}

	// Download speaker embedding model
	if force || !status.SpeakerEmbeddingReady {
		fmt.Println("Downloading speaker embedding model...")
		speakerPath := filepath.Join(m.modelDir, SpeakerEmbeddingFile)
		if err := downloadFile(SpeakerEmbeddingURL, speakerPath); err != nil {
			return fmt.Errorf("downloading speaker embedding model: %w", err)
		}
		fmt.Println("Speaker embedding model downloaded.")
	} else {
		fmt.Println("Speaker embedding model already present, skipping.")
	}

	// Download Pyannote speaker segmentation model (for diarization)
	if force || !status.SpeakerSegmentationReady {
		fmt.Println("Downloading Pyannote speaker segmentation model...")
		if err := m.downloadAndExtractArchive(PyannoteSegmentationURL); err != nil {
			return fmt.Errorf("downloading Pyannote segmentation model: %w", err)
		}
		fmt.Println("Pyannote segmentation model downloaded.")
	} else {
		fmt.Println("Pyannote segmentation model already present, skipping.")
	}

	// Download English streaming Zipformer (realtime pass). Not required
	// for Ready() below — a failure here is logged but non-fatal, since
	// the live pipeline already falls back gracefully without it.
	if force || !status.EnglishStreamingReady {
		fmt.Println("Downloading English streaming Zipformer model (realtime transcription pass)...")
		if err := m.downloadAndExtractArchive(EnglishStreamingArchiveURL); err != nil {
			fmt.Printf("Warning: failed to download English streaming model: %v (live transcription will fall back to non-realtime mode)\n", err)
		} else {
			fmt.Println("English streaming Zipformer model downloaded and extracted.")
		}
	} else {
		fmt.Println("English streaming Zipformer model already present, skipping.")
	}

	// Verify
	final := m.Check()
	if !final.Ready() {
		return fmt.Errorf("model verification failed after download")
	}

	return nil
}

// DownloadSpeakerModel downloads the speaker embedding model.
// If force is true, re-downloads even if already present.
func (m *Manager) DownloadSpeakerModel(force bool) error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}

	status := m.Check()
	if !force && status.SpeakerEmbeddingReady {
		fmt.Println("Speaker embedding model already present, skipping.")
		return nil
	}

	fmt.Println("Downloading speaker embedding model...")
	speakerPath := filepath.Join(m.modelDir, SpeakerEmbeddingFile)
	if err := downloadFile(SpeakerEmbeddingURL, speakerPath); err != nil {
		return fmt.Errorf("downloading speaker embedding model: %w", err)
	}
	fmt.Println("Speaker embedding model downloaded.")
	return nil
}

// DownloadMultilingual downloads Bengali model for multilingual support.
// Whisper tiny (lang-id) is no longer downloaded — language selection is manual.
// If force is true, re-downloads even if already present.
func (m *Manager) DownloadMultilingual(force bool) error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}

	status := m.Check()

	// Download Bengali Zipformer transducer
	if force || !status.BengaliReady {
		fmt.Println("Downloading Bengali Zipformer model...")
		if err := m.downloadAndExtractArchive(BengaliArchiveURL); err != nil {
			return fmt.Errorf("downloading Bengali model: %w", err)
		}
		fmt.Println("Bengali Zipformer model downloaded and extracted.")
	} else {
		fmt.Println("Bengali Zipformer model already present, skipping.")
	}

	// Verify
	final := m.Check()
	if !final.MultilingualReady() {
		return fmt.Errorf("multilingual model verification failed after download")
	}

	return nil
}

// downloadAndExtractArchive downloads a tar.bz2 archive and extracts it to the model directory.
func (m *Manager) downloadAndExtractArchive(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	bar := progressbar.DefaultBytes(resp.ContentLength, "downloading")
	reader := io.TeeReader(resp.Body, bar)

	return extractTarBz2(reader, m.modelDir)
}

// extractTarBz2 extracts a tar.bz2 stream to the destination directory.
func extractTarBz2(r io.Reader, destDir string) error {
	bzReader := bzip2.NewReader(r)
	tarReader := tar.NewReader(bzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Sanitize path to prevent directory traversal
		cleanName := filepath.Clean(header.Name)
		if strings.Contains(cleanName, "..") {
			return fmt.Errorf("tar contains path traversal: %s", header.Name)
		}

		target := filepath.Join(destDir, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent directory for %s: %w", target, err)
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}

			if _, err := io.Copy(f, tarReader); err != nil {
				_ = f.Close()
				return fmt.Errorf("writing file %s: %w", target, err)
			}
			_ = f.Close()
		}
	}

	return nil
}

// downloadFile downloads a URL to a local file with a progress bar.
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer func() { _ = f.Close() }()

	bar := progressbar.DefaultBytes(resp.ContentLength, "downloading")
	if _, err := io.Copy(io.MultiWriter(f, bar), resp.Body); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func allFilesExist(paths ...string) bool {
	for _, p := range paths {
		if !fileExists(p) {
			return false
		}
	}
	return true
}
