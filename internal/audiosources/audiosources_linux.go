package audiosources

// ListActive has no Linux implementation: Linux's monitor-source
// picker is internal/audio's PulseAudio device-name listing, a
// completely different (and already-working) mechanism. This exists
// only so callers don't need runtime.GOOS checks.
func ListActive() ([]Source, error) {
	return nil, nil
}
