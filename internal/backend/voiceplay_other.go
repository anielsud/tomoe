//go:build !darwin

package backend

// StartVoiceClip returns 0: outside macOS voice clips play in the webview
// (PlayVoiceClip).
func (a *App) StartVoiceClip(clip VoiceClip) (int, error) { return 0, nil }

// StopVoiceClip does nothing outside macOS.
func (a *App) StopVoiceClip() {}
