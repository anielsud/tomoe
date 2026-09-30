package session

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
)

// SaveAudioMP3 converts float32 PCM samples to an MP3 file via ffmpeg.
// It writes a temporary WAV file, then converts to MP3 using libmp3lame quality 6 (~115kbps).
func SaveAudioMP3(samples []float32, sampleRate int, path string) error {
	if len(samples) == 0 {
		return fmt.Errorf("no audio samples to save")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write temporary WAV file
	tmpWAV, err := os.CreateTemp("", "tomoe-audio-*.wav")
	if err != nil {
		return fmt.Errorf("creating temp WAV file: %w", err)
	}
	tmpPath := tmpWAV.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := writeWAV(tmpWAV, samples, sampleRate); err != nil {
		_ = tmpWAV.Close()
		return fmt.Errorf("writing WAV: %w", err)
	}
	_ = tmpWAV.Close()

	// Convert to MP3 via ffmpeg
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, "-y",
		"-i", tmpPath,
		"-codec:a", "libmp3lame",
		"-qscale:a", "6",
		path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg conversion: %w: %s", err, string(output))
	}

	return nil
}

// SaveAudioM4A saves one or more audio tracks as an M4A (MP4+AAC) file via ffmpeg.
// For dual-source sessions: tracks[0]=mic, tracks[1]=monitor (separate AAC streams).
// For single-source sessions: tracks[0]=the single source.
func SaveAudioM4A(tracks [][]float32, sampleRate int, path string) error {
	if len(tracks) == 0 {
		return fmt.Errorf("no audio tracks to save")
	}
	for i, t := range tracks {
		if len(t) == 0 {
			return fmt.Errorf("track %d has no samples", i)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write each track to a temporary WAV file
	tmpPaths := make([]string, len(tracks))
	for i, samples := range tracks {
		tmpWAV, err := os.CreateTemp("", fmt.Sprintf("tomoe-track%d-*.wav", i))
		if err != nil {
			cleanupTempFiles(tmpPaths[:i])
			return fmt.Errorf("creating temp WAV for track %d: %w", i, err)
		}
		tmpPaths[i] = tmpWAV.Name()

		if err := writeWAV(tmpWAV, samples, sampleRate); err != nil {
			_ = tmpWAV.Close()
			cleanupTempFiles(tmpPaths[:i+1])
			return fmt.Errorf("writing WAV for track %d: %w", i, err)
		}
		_ = tmpWAV.Close()
	}
	defer cleanupTempFiles(tmpPaths)

	// Build ffmpeg command
	args := []string{"-y"}
	for _, p := range tmpPaths {
		args = append(args, "-i", p)
	}

	if len(tracks) >= 2 {
		// Dual-source: 3 output tracks — merged (default), mic, monitor
		// Track 0: merged mix of all inputs (default — plays in media players)
		// Track 1: mic only
		// Track 2: monitor only
		args = append(args,
			"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=longest[mixed]",
			"-map", "[mixed]", "-map", "0:a", "-map", "1:a",
			"-c:a", "aac", "-b:a", "128k",
			"-metadata:s:a:0", "title=mixed",
			"-metadata:s:a:1", "title=mic",
			"-metadata:s:a:2", "title=monitor",
			"-disposition:a:0", "default",
			"-disposition:a:1", "0",
			"-disposition:a:2", "0",
		)
	} else {
		// Single-source: 1 track
		args = append(args, "-map", "0:a", "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, path)

	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg M4A encoding: %w: %s", err, string(output))
	}

	return nil
}

func cleanupTempFiles(paths []string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

// DecodeTrackToFloat32 extracts a single audio track from a multi-track file
// and decodes it to 16kHz mono float32 PCM using ffmpeg.
// track is 0-indexed. A dual-source session's M4A holds 0=mixed, 1=mic,
// 2=monitor; a single-source one holds just track 0 (see SaveAudioM4A).
func DecodeTrackToFloat32(path string, track int) ([]float32, error) {
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(ffmpeg,
		"-i", path,
		"-map", fmt.Sprintf("0:a:%d", track),
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", "1",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	data, err := io.ReadAll(stdout)
	if err != nil {
		return nil, fmt.Errorf("reading ffmpeg output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg exited with error: %w", err)
	}

	numSamples := len(data) / 2
	samples := make([]float32, numSamples)
	for i := range numSamples {
		s16 := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
		samples[i] = float32(s16) / float32(math.MaxInt16)
	}

	return samples, nil
}

// IsM4A returns true if the path ends with .m4a.
func IsM4A(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".m4a")
}

// writeWAV writes float32 PCM samples as a 16-bit WAV file.
func writeWAV(f *os.File, samples []float32, sampleRate int) error {
	numChannels := 1
	bitsPerSample := 16
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := len(samples) * blockAlign

	// RIFF header
	if _, err := f.Write([]byte("RIFF")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(36+dataSize)); err != nil {
		return err
	}
	if _, err := f.Write([]byte("WAVE")); err != nil {
		return err
	}

	// fmt chunk
	if _, err := f.Write([]byte("fmt ")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(16)); err != nil { // chunk size
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint16(1)); err != nil { // PCM format
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint16(numChannels)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(sampleRate)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(byteRate)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint16(blockAlign)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint16(bitsPerSample)); err != nil {
		return err
	}

	// data chunk
	if _, err := f.Write([]byte("data")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(dataSize)); err != nil {
		return err
	}

	// Write samples as 16-bit signed integers
	for _, sample := range samples {
		// Clamp to [-1.0, 1.0]
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}
		s16 := int16(math.Round(float64(sample) * 32767.0))
		if err := binary.Write(f, binary.LittleEndian, s16); err != nil {
			return err
		}
	}

	return nil
}
