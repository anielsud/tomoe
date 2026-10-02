package videohint

import (
	"fmt"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/ocr"
)

// Text reading for video hints runs internal/ocr's open models (PaddleOCR
// text detection and recognition through ONNX Runtime), the same on every
// platform. The models are loaded on first use from the paths SetOCRModels
// gives.

var (
	ocrMu       sync.Mutex
	ocrDetPath  string
	ocrRecPath  string
	ocrReader   *ocr.Reader
	ocrOpenErr  error
	ocrOpenedAt [2]string // the paths ocrReader (or ocrOpenErr) is for
)

// SetOCRModels sets the text detection and recognition models to read
// with. Changing them reloads on next use.
func SetOCRModels(detPath, recPath string) {
	ocrMu.Lock()
	defer ocrMu.Unlock()
	ocrDetPath, ocrRecPath = detPath, recPath
}

func textReader() (*ocr.Reader, error) {
	ocrMu.Lock()
	defer ocrMu.Unlock()
	want := [2]string{ocrDetPath, ocrRecPath}
	if want == ocrOpenedAt && (ocrReader != nil || ocrOpenErr != nil) {
		return ocrReader, ocrOpenErr
	}
	if ocrReader != nil {
		ocrReader.Close()
		ocrReader = nil
	}
	ocrOpenedAt = want
	if ocrDetPath == "" || ocrRecPath == "" {
		ocrOpenErr = fmt.Errorf("videohint: text-reading models not set up (download them from Tools)")
		return nil, ocrOpenErr
	}
	ocrReader, ocrOpenErr = ocr.OpenReader(ocrDetPath, ocrRecPath)
	return ocrReader, ocrOpenErr
}

// RecognizeText returns all the text in pix (packed RGB, 3 bytes per pixel,
// no row padding), its lines joined by spaces.
func RecognizeText(pix []byte, width, height int) (string, error) {
	r, err := textReader()
	if err != nil {
		return "", err
	}
	return r.ReadText(pix, width, height)
}

// recognizeName returns the widest line of text in pix: a name label's
// name, not the icons or the stray captions beside it.
func recognizeName(pix []byte, width, height int) (string, error) {
	r, err := textReader()
	if err != nil {
		return "", err
	}
	lines, err := r.ReadLines(pix, width, height)
	if err != nil {
		return "", err
	}
	best, bw := "", 0
	for _, l := range lines {
		if l.W > bw {
			best, bw = l.Text, l.W
		}
	}
	return best, nil
}
