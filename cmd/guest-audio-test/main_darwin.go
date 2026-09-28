// guest-audio-test: diagnostic tool to test the ScreenCaptureKit guest-audio
// cgo bridge (internal/guestaudio) on macOS — captures a few seconds from
// the active Teams meeting window and reports peak/RMS, mirroring
// tomoe-darwin's Python spike's own validation (guest_audio_tap/sck_capture.py)
// so the two are directly comparable.
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/teamsvideo"

	"github.com/sosuke-ai/tomoe-pc/internal/guestaudio"
)

func main() {
	windowID, err := teamsvideo.FindMeetingWindow()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FindMeetingWindow: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Teams window: %d\n", windowID)

	var (
		mu         sync.Mutex
		allSamples []float32
		callbacks  int
		sampleRate float64
	)

	tap := guestaudio.NewTap(uint32(windowID), func(samples []float32, sr float64) {
		mu.Lock()
		allSamples = append(allSamples, samples...)
		callbacks++
		sampleRate = sr
		mu.Unlock()
	})

	if err := tap.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Start: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Capturing 8 seconds...")

	dur := 8 * time.Second
	start := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for time.Since(start) < dur {
		<-ticker.C
		mu.Lock()
		n := len(allSamples)
		cb := callbacks
		mu.Unlock()
		fmt.Printf("  %.1fs: %d callbacks, %d samples so far\n", time.Since(start).Seconds(), cb, n)
	}

	tap.Stop()

	mu.Lock()
	final := allSamples
	sr := sampleRate
	mu.Unlock()

	fmt.Printf("\nTotal: %d callbacks, %d samples", callbacks, len(final))
	if sr > 0 {
		fmt.Printf(" (%.1f seconds at %.0fHz)", float64(len(final))/sr, sr)
	}
	fmt.Println()
	fmt.Printf("Peak amplitude: %.6f\n", peak(final))
	fmt.Printf("RMS: %.6f\n", rms(final))

	if len(final) == 0 {
		fmt.Fprintln(os.Stderr, "\nNo audio captured!")
		os.Exit(1)
	}

	if err := writeF32("guest_raw.pcm", final); err != nil {
		fmt.Fprintf(os.Stderr, "writing guest_raw.pcm: %v\n", err)
	} else {
		fmt.Println("Wrote guest_raw.pcm")
	}
}

func peak(s []float32) float32 {
	var p float32
	for _, v := range s {
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}

func rms(s []float32) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

func writeF32(path string, samples []float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, s := range samples {
		if err := binary.Write(f, binary.LittleEndian, s); err != nil {
			return err
		}
	}
	return f.Close()
}
