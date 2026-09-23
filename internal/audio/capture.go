package audio

// CaptureSampleRate is the sample rate every audio source in the
// pipeline is expected to deliver: Parakeet TDT's native input rate
// (internal/transcribe/parakeet.go's sampleRate const). Sources that
// don't natively capture at this rate (e.g. guestaudio's ScreenCaptureKit
// tap, which delivers 48kHz) must resample down to it themselves — see
// Resample in dsp.go.
const CaptureSampleRate = 16000

// DeviceType indicates whether a device is a regular input or a monitor source.
type DeviceType int

const (
	// Input is a regular audio input device (microphone).
	Input DeviceType = iota
	// Monitor is a PulseAudio/PipeWire monitor source (system audio loopback).
	Monitor
)

// DeviceInfo describes an audio input device.
type DeviceInfo struct {
	ID         string
	Name       string
	IsDefault  bool
	DeviceType DeviceType
}

// Capturer captures audio from a microphone.
type Capturer interface {
	// Start begins capturing audio.
	Start() error

	// Stop stops capturing audio.
	Stop() error

	// Samples returns the captured audio as float32 PCM at 16kHz mono.
	Samples() []float32

	// Reset clears the captured audio buffer.
	Reset()

	// Close releases all resources.
	Close()
}

// ListDevices returns available audio capture devices.
func ListDevices() ([]DeviceInfo, error) {
	return listDevices()
}

// NewCapturer creates a Capturer for the specified device.
// Pass "default" or empty string for the default device.
// deviceType indicates whether this is a regular Input or a Monitor source.
func NewCapturer(device string, deviceType DeviceType) (Capturer, error) {
	return newCapturer(device, deviceType)
}
