package models

import (
	"path/filepath"
	"testing"
)

func TestLiveModelChoice(t *testing.T) {
	if got := ResolveLiveModel("").ID; got != LiveFastConformer {
		t.Errorf("default live model %q, want %q", got, LiveFastConformer)
	}
	if got := ResolveLiveModel("auto").ID; got != LiveFastConformer {
		t.Errorf("auto live model %q", got)
	}
	if got := ResolveLiveModel("no-such-model").ID; got != LiveFastConformer {
		t.Errorf("unknown live model resolved to %q", got)
	}
	dir := t.TempDir()
	s := NewManager(dir).WithLiveModel(LiveZipformer).Check()
	if s.EnglishStreamingModelType != "zipformer2" || filepath.Dir(s.EnglishStreamingEncoderPath) != filepath.Join(dir, EnglishStreamingSubdir) {
		t.Errorf("zipformer paths/type: %s %s", s.EnglishStreamingEncoderPath, s.EnglishStreamingModelType)
	}
	s = NewManager(dir).Check()
	if s.EnglishStreamingModelType != "" || filepath.Base(filepath.Dir(s.EnglishStreamingEncoderPath)) != LiveModels[0].Subdir {
		t.Errorf("default paths/type: %s %q", s.EnglishStreamingEncoderPath, s.EnglishStreamingModelType)
	}
}
