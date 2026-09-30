package transcribe

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/toolpath"
)

// supportedExtensions lists audio formats supported for transcription.
var supportedExtensions = map[string]bool{
	".wav":  true,
	".flac": true,
	".ogg":  true,
	".mp3":  true,
	".m4a":  true,
}

// IsSupportedFormat checks if the file extension is a supported audio format.
func IsSupportedFormat(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return supportedExtensions[ext]
}

// convertToWAV converts a non-WAV audio file to 16kHz mono WAV using ffmpeg.
// Returns the path to the temporary WAV file (caller must remove it).
func convertToWAV(srcPath string) (string, error) {
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return "", fmt.Errorf("transcribing non-WAV files needs ffmpeg: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "tomoe-*.wav")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()

	cmd := exec.Command(ffmpeg,
		"-i", srcPath,
		"-ar", "16000",
		"-ac", "1",
		"-sample_fmt", "s16",
		"-y",
		tmpPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg conversion failed: %w\n%s", err, output)
	}

	return tmpPath, nil
}

// needsConversion returns true if the file is not a WAV and needs ffmpeg conversion.
func needsConversion(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext != ".wav" && supportedExtensions[ext]
}
