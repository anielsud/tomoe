//go:build darwin

package backend

import (
	"os"
	"os/exec"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// On macOS voice clips play through afplay rather than the webview. The
// webview's audio stays on the output device that was the default when
// Tomoe started, so after switching to a headset or speakerphone the clips
// went to the laptop's speakers; afplay opens the current default output
// each time.

var voicePlay struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	last int
}

// StartVoiceClip plays a voice clip, stopping any clip already playing.
// It returns the playback's id, sent with "voices:clip-ended" when it
// stops, or 0 when clips play in the webview instead (PlayVoiceClip).
func (a *App) StartVoiceClip(clip VoiceClip) (int, error) {
	wav, err := a.cutVoiceClip(clip)
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "tomoe-voice-*.wav")
	if err != nil {
		return 0, err
	}
	_, err = f.Write(wav)
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return 0, err
	}
	a.StopVoiceClip()
	cmd := exec.Command("/usr/bin/afplay", f.Name())
	if err := cmd.Start(); err != nil {
		os.Remove(f.Name())
		return 0, err
	}
	voicePlay.mu.Lock()
	voicePlay.last++
	id := voicePlay.last
	voicePlay.cmd = cmd
	voicePlay.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		os.Remove(f.Name())
		voicePlay.mu.Lock()
		if voicePlay.cmd == cmd {
			voicePlay.cmd = nil
		}
		voicePlay.mu.Unlock()
		wailsRuntime.EventsEmit(a.ctx, "voices:clip-ended", id)
	}()
	return id, nil
}

// StopVoiceClip stops the clip StartVoiceClip is playing, if any.
func (a *App) StopVoiceClip() {
	voicePlay.mu.Lock()
	cmd := voicePlay.cmd
	voicePlay.cmd = nil
	voicePlay.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
