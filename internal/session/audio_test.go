package session

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAudioMP3(t *testing.T) {
	// Skip if ffmpeg is not available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping MP3 test")
	}

	// Generate 1 second of silence at 16kHz
	sampleRate := 16000
	samples := make([]float32, sampleRate)

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test.mp3")

	if err := SaveAudioMP3(samples, sampleRate, outPath); err != nil {
		t.Fatalf("SaveAudioMP3() error: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file not found: %v", err)
	}

	if info.Size() == 0 {
		t.Error("output MP3 file is empty")
	}
}

func TestSaveAudioMP3EmptySamples(t *testing.T) {
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test.mp3")

	err := SaveAudioMP3(nil, 16000, outPath)
	if err == nil {
		t.Error("SaveAudioMP3() should fail with empty samples")
	}
}

func TestWriteWAV(t *testing.T) {
	// Generate a short sine-like signal
	samples := []float32{0, 0.5, 1.0, 0.5, 0, -0.5, -1.0, -0.5}

	tmpFile, err := os.CreateTemp(t.TempDir(), "test-*.wav")
	if err != nil {
		t.Fatalf("CreateTemp error: %v", err)
	}
	defer func() { _ = tmpFile.Close() }()

	if err := writeWAV(tmpFile, samples, 16000); err != nil {
		t.Fatalf("writeWAV error: %v", err)
	}

	info, err := os.Stat(tmpFile.Name())
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}

	// WAV header is 44 bytes + 2 bytes per sample
	expectedSize := int64(44 + len(samples)*2)
	if info.Size() != expectedSize {
		t.Errorf("WAV file size = %d, want %d", info.Size(), expectedSize)
	}
}

// The WAV is byte for byte what the previous per-sample writer produced
// (header, rounding and clamping), and a half-hour track saves in well
// under a second instead of minutes.
func TestWriteWAVSameBytesFast(t *testing.T) {
	samples := []float32{0, 0.5, 1.0, 1.5, -0.25, -1.0, -3, 0.33333, -0.000001, 0.99999}
	var want bytes.Buffer
	le := binary.LittleEndian
	want.WriteString("RIFF")
	_ = binary.Write(&want, le, uint32(36+len(samples)*2))
	want.WriteString("WAVEfmt ")
	_ = binary.Write(&want, le, uint32(16))
	_ = binary.Write(&want, le, uint16(1))
	_ = binary.Write(&want, le, uint16(1))
	_ = binary.Write(&want, le, uint32(16000))
	_ = binary.Write(&want, le, uint32(32000))
	_ = binary.Write(&want, le, uint16(2))
	_ = binary.Write(&want, le, uint16(16))
	want.WriteString("data")
	_ = binary.Write(&want, le, uint32(len(samples)*2))
	for _, s := range samples {
		s = float32(math.Max(-1, math.Min(1, float64(s))))
		_ = binary.Write(&want, le, int16(math.Round(float64(s)*32767.0)))
	}
	f, err := os.CreateTemp(t.TempDir(), "same-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(f, samples, 16000); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("bytes differ:\n got %x\nwant %x", got, want.Bytes())
	}

	long := make([]float32, 30*60*16000)
	for i := range long {
		long[i] = float32(math.Sin(float64(i) / 20))
	}
	f2, err := os.CreateTemp(t.TempDir(), "long-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := writeWAV(f2, long, 16000); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("a half-hour track took %s", d)
	}
	_ = f2.Close()
}
