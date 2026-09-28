// video-signal-test: diagnostic tool to test the Teams meeting window
// capture cgo bridge (internal/teamsvideo) on macOS — confirms window
// discovery and frame capture work before building ring detection/OCR on
// top of them.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"
)

func main() {
	id, err := teamsvideo.FindMeetingWindow()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FindMeetingWindow: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Found Teams meeting window: %d\n", id)

	frame, err := teamsvideo.CaptureWindowRGB(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CaptureWindowRGB: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Captured frame: %dx%d (%d bytes)\n", frame.Width, frame.Height, len(frame.Pix))

	img := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	for y := 0; y < frame.Height; y++ {
		for x := 0; x < frame.Width; x++ {
			i := (y*frame.Width + x) * 3
			img.Set(x, y, color.RGBA{R: frame.Pix[i], G: frame.Pix[i+1], B: frame.Pix[i+2], A: 255})
		}
	}

	out, err := os.Create("capture_test.png")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating output file: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		fmt.Fprintf(os.Stderr, "encoding PNG: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Wrote capture_test.png")
}
