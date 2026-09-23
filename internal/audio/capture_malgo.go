package audio

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"github.com/gen2brain/malgo"
)

const captureSampleRate = 16000

// malgoCapturer implements Capturer using miniaudio via malgo.
type malgoCapturer struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device

	mu      sync.Mutex
	samples []float32
}

func listDevices() ([]DeviceInfo, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("initializing audio context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("enumerating capture devices: %w", err)
	}

	devices := make([]DeviceInfo, len(infos))
	for i, info := range infos {
		name := info.Name()
		dt := Input
		nameLower := strings.ToLower(name)
		if strings.Contains(nameLower, ".monitor") || strings.HasPrefix(nameLower, "monitor of ") {
			dt = Monitor
		}
		devices[i] = DeviceInfo{
			ID:         info.ID.String(),
			Name:       name,
			IsDefault:  info.IsDefault != 0,
			DeviceType: dt,
		}
	}
	return devices, nil
}

func newCapturer(device string, deviceType DeviceType) (Capturer, error) {
	// For monitor sources, force PulseAudio backend — monitor sources are a
	// PulseAudio concept and only appear under the PulseAudio backend.
	// On PipeWire systems this works via the PulseAudio compatibility layer.
	var backends []malgo.Backend
	if deviceType == Monitor {
		backends = []malgo.Backend{malgo.BackendPulseaudio}
	}
	ctx, err := malgo.InitContext(backends, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("initializing audio context: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = captureSampleRate
	deviceConfig.Alsa.NoMMap = 1 // PipeWire compatibility

	// Use specific device if not "default" or empty
	if device != "" && device != "default" {
		infos, err := ctx.Devices(malgo.Capture)
		if err != nil {
			_ = ctx.Uninit()
			ctx.Free()
			return nil, fmt.Errorf("enumerating devices: %w", err)
		}

		found := false
		for _, info := range infos {
			if info.Name() == device || info.ID.String() == device {
				id := info.ID
				deviceConfig.Capture.DeviceID = id.Pointer()
				found = true
				break
			}
		}
		if !found {
			_ = ctx.Uninit()
			ctx.Free()
			return nil, fmt.Errorf("audio device not found: %s", device)
		}
	}

	c := &malgoCapturer{
		ctx: ctx,
	}

	callbacks := malgo.DeviceCallbacks{
		Data: c.onData,
	}

	dev, err := malgo.InitDevice(ctx.Context, deviceConfig, callbacks)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("initializing capture device: %w", err)
	}
	c.device = dev

	return c, nil
}

// onData is called by malgo for each audio frame period.
func (c *malgoCapturer) onData(_, pInput []byte, framecount uint32) {
	// Convert S16 little-endian to float32
	numSamples := int(framecount)
	if numSamples*2 > len(pInput) {
		numSamples = len(pInput) / 2
	}

	c.mu.Lock()
	for i := 0; i < numSamples; i++ {
		s16 := int16(binary.LittleEndian.Uint16(pInput[i*2 : i*2+2]))
		c.samples = append(c.samples, float32(s16)/32768.0)
	}
	c.mu.Unlock()
}

func (c *malgoCapturer) Start() error {
	return c.device.Start()
}

func (c *malgoCapturer) Stop() error {
	return c.device.Stop()
}

func (c *malgoCapturer) Samples() []float32 {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]float32, len(c.samples))
	copy(out, c.samples)
	return out
}

func (c *malgoCapturer) Reset() {
	c.mu.Lock()
	c.samples = c.samples[:0]
	c.mu.Unlock()
}

func (c *malgoCapturer) Close() {
	if c.device != nil {
		c.device.Uninit()
		c.device = nil
	}
	if c.ctx != nil {
		_ = c.ctx.Uninit()
		c.ctx.Free()
		c.ctx = nil
	}
}
