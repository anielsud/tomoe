package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSpeakerModel(t *testing.T) {
	for _, tc := range []struct{ setting, lang, want string }{
		{"auto", "en", SpeakerModelEres2NetEn},
		{"", "", SpeakerModelEres2NetEn},
		{"auto", "bn", SpeakerModelEres2Net},
		{SpeakerModelEres2Net, "en", SpeakerModelEres2Net},
		{SpeakerModelEres2NetEn, "bn", SpeakerModelEres2NetEn},
		{"bogus", "bn", SpeakerModelEres2Net},
	} {
		if got := ResolveSpeakerModel(tc.setting, tc.lang).ID; got != tc.want {
			t.Errorf("ResolveSpeakerModel(%q, %q) = %s, want %s", tc.setting, tc.lang, got, tc.want)
		}
	}
	if got := SpeakerModelsNeeded("auto", []string{"en", "bn"}); len(got) != 2 {
		t.Errorf("auto for en+bn needs %d models, want 2", len(got))
	}
}

func TestSpeakerModelForFallsBackToBase(t *testing.T) {
	dir := t.TempDir()
	s := NewManager(dir).Check()
	if m, _, fellBack := s.SpeakerModelFor("auto", "en"); m.ID != SpeakerModelEres2Net || !fellBack {
		t.Errorf("without the English model got %s (fell back %v), want the base model", m.ID, fellBack)
	}
	en, _ := SpeakerModelByID(SpeakerModelEres2NetEn)
	if err := os.WriteFile(filepath.Join(dir, en.File), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if m, path, fellBack := s.SpeakerModelFor("auto", "en"); m.ID != en.ID || fellBack || path != filepath.Join(dir, en.File) {
		t.Errorf("with the English model got %s at %s (fell back %v)", m.ID, path, fellBack)
	}
}
