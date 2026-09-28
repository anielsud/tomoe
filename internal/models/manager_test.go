package models

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	status := m.Check()

	if status.ParakeetReady {
		t.Error("ParakeetReady = true for empty dir")
	}
	if status.VADReady {
		t.Error("VADReady = true for empty dir")
	}
	if status.Ready() {
		t.Error("Ready() = true for empty dir")
	}
	if status.ModelDir != dir {
		t.Errorf("ModelDir = %q, want %q", status.ModelDir, dir)
	}
}

func TestCheckCompleteModels(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	createFakeModelFiles(t, dir)

	status := m.Check()

	if !status.ParakeetReady {
		t.Error("ParakeetReady = false with all files present")
	}
	if !status.VADReady {
		t.Error("VADReady = false with VAD file present")
	}
	if !status.Ready() {
		t.Error("Ready() = false with all files present")
	}
}

func TestCheckPartialModels(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	// Create only some Parakeet files (missing joiner)
	parakeetDir := filepath.Join(dir, ParakeetSubdir)
	if err := os.MkdirAll(parakeetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{encoderFile, decoderFile, tokensFile} {
		if err := os.WriteFile(filepath.Join(parakeetDir, name), []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	status := m.Check()

	if status.ParakeetReady {
		t.Error("ParakeetReady = true with missing joiner file")
	}
}

func TestCheckVADOnlyPresent(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	if err := os.WriteFile(filepath.Join(dir, SileroVADFile), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := m.Check()

	if status.ParakeetReady {
		t.Error("ParakeetReady = true with no Parakeet files")
	}
	if !status.VADReady {
		t.Error("VADReady = false with VAD file present")
	}
	if status.Ready() {
		t.Error("Ready() = true with only VAD present")
	}
}

func TestStatusString(t *testing.T) {
	s := &Status{
		ModelDir:      "/test/models",
		ParakeetReady: true,
		VADReady:      false,
	}

	str := s.String()
	if !strings.Contains(str, "ready") {
		t.Errorf("String() missing 'ready': %s", str)
	}
	if !strings.Contains(str, "not downloaded") {
		t.Errorf("String() missing 'not downloaded': %s", str)
	}
}

func TestExtractTarBz2(t *testing.T) {
	archive := createTestTarBz2(t, map[string]string{
		"testdir/file1.txt": "hello",
		"testdir/file2.txt": "world",
	})

	destDir := t.TempDir()
	if err := extractTarBz2(bytes.NewReader(archive), destDir); err != nil {
		t.Fatalf("extractTarBz2() error: %v", err)
	}

	for _, tc := range []struct {
		path    string
		content string
	}{
		{"testdir/file1.txt", "hello"},
		{"testdir/file2.txt", "world"},
	} {
		data, err := os.ReadFile(filepath.Join(destDir, tc.path))
		if err != nil {
			t.Errorf("reading %s: %v", tc.path, err)
			continue
		}
		if string(data) != tc.content {
			t.Errorf("%s content = %q, want %q", tc.path, string(data), tc.content)
		}
	}
}

func TestExtractTarBz2PathTraversal(t *testing.T) {
	archive := createTestTarBz2(t, map[string]string{
		"../escape.txt": "malicious",
	})

	destDir := t.TempDir()
	err := extractTarBz2(bytes.NewReader(archive), destDir)
	if err == nil {
		t.Error("extractTarBz2() should reject path traversal")
	}
}

func TestDownloadFile(t *testing.T) {
	content := []byte("test model data")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "model.onnx")

	if err := downloadFile(server.URL, destPath); err != nil {
		t.Fatalf("downloadFile() error: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("downloaded content = %q, want %q", data, content)
	}
}

func TestDownloadFileHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "model.onnx")

	err := downloadFile(server.URL, destPath)
	if err == nil {
		t.Error("downloadFile() should return error for 404")
	}
}

func TestModelDir(t *testing.T) {
	m := NewManager("/test/path")
	if m.ModelDir() != "/test/path" {
		t.Errorf("ModelDir() = %q, want %q", m.ModelDir(), "/test/path")
	}
}

func TestDownloadSkipsExistingModels(t *testing.T) {
	// Create a server that tracks requests
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data"))
	}))
	defer server.Close()

	dir := t.TempDir()
	m := NewManager(dir)

	// Pre-populate all model files
	createFakeModelFiles(t, dir)

	// Download with force=false should skip
	err := m.Download(false)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if requestCount != 0 {
		t.Errorf("Download(force=false) made %d HTTP requests, want 0", requestCount)
	}
}

// --- helpers ---

func createFakeModelFiles(t *testing.T, dir string) {
	t.Helper()

	// Parakeet model files
	parakeetDir := filepath.Join(dir, ParakeetSubdir)
	if err := os.MkdirAll(parakeetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{encoderFile, decoderFile, joinerFile, tokensFile} {
		if err := os.WriteFile(filepath.Join(parakeetDir, name), []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Silero VAD
	if err := os.WriteFile(filepath.Join(dir, SileroVADFile), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Speaker embedding
	if err := os.WriteFile(filepath.Join(dir, SpeakerEmbeddingFile), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pyannote segmentation
	segDir := filepath.Join(dir, PyannoteSegmentationSubdir)
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, PyannoteSegmentationFile), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createTestTarBz2(t *testing.T, files map[string]string) []byte {
	t.Helper()

	// Create tar archive in memory
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)

	// Collect directories
	dirs := map[string]bool{}
	for name := range files {
		dir := filepath.Dir(name)
		if dir != "." && !dirs[dir] {
			dirs[dir] = true
			if err := tw.WriteHeader(&tar.Header{
				Name:     dir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(content)),
			Mode:     0o644,
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Compress tar data with bzip2 using the system command
	// (Go stdlib only has bzip2 decompressor)
	cmd := exec.Command("bzip2", "-c")
	cmd.Stdin = &tarBuf
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Skipf("bzip2 command not available: %v", err)
	}
	return out.Bytes()
}

func TestPruneEnglishStreamingKeepsOnlyInt8Model(t *testing.T) {
	modelDir := t.TempDir()
	dir := filepath.Join(modelDir, EnglishStreamingSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := []string{englishStreamingEncoderFile, englishStreamingDecoderFile, englishStreamingJoinerFile, englishStreamingTokensFile, "README.md"}
	drop := []string{
		"encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
	}
	for _, name := range append(append([]string{}, keep...), drop...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pruneEnglishStreaming(dir)

	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	for _, name := range drop {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present (err=%v), want removed", name, err)
		}
	}
	if !NewManager(modelDir).Check().EnglishStreamingReady {
		t.Error("streaming model no longer complete after pruning")
	}
}
