package videohint

import "fmt"

// RecognizeText has no implementation on Linux — Vision.framework is
// macOS-only. Never called there in practice (the whole videohint
// poller is already a no-op on Linux, see poller_linux.go), but kept
// so cross-platform callers don't need runtime.GOOS checks.
func RecognizeText(pix []byte, width, height int) (string, error) {
	return "", fmt.Errorf("videohint: OCR not supported on this platform")
}
