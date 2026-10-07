package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveASRModel(t *testing.T) {
	for _, c := range []struct{ setting, lang, want string }{
		{"auto", "en", ASRModelCohere},
		{"", "", ASRModelCohere},
		{"auto", "bn", ASRModelParakeetV3},
		{"auto", "de", ASRModelParakeetV3},
		{"parakeet-v3", "en", ASRModelParakeetV3},
		{"parakeet-v2-en", "en", ASRModelParakeetV2En},
		{"nonsense", "en", ASRModelCohere},
	} {
		if got := ResolveASRModel(c.setting, c.lang).ID; got != c.want {
			t.Errorf("ResolveASRModel(%q, %q) = %s, want %s", c.setting, c.lang, got, c.want)
		}
	}
}

func TestASRModelsNeeded(t *testing.T) {
	if got := ASRModelsNeeded("parakeet-v3", []string{"en", "bn"}); len(got) != 0 {
		t.Errorf("parakeet-v3 needs nothing beyond the base download, got %v", got)
	}
	if got := ASRModelsNeeded("auto", []string{"en", "bn"}); len(got) != 1 || got[0].ID != ASRModelCohere {
		t.Errorf("auto with English needs Cohere, got %v", got)
	}
}

func TestASRModelForFallsBackWhenMissing(t *testing.T) {
	s := &Status{ModelDir: t.TempDir()}
	if m, fellBack := s.ASRModelFor("auto", "en"); m.ID != ASRModelParakeetV3 || !fellBack {
		t.Errorf("Cohere missing: got %s, fellBack %v; want v3, true", m.ID, fellBack)
	}
	co, _ := ASRModelByID(ASRModelCohere)
	dir := s.ASRModelDir(co)
	for _, f := range []string{filepath.Join(dir, "encoder.int8.onnx"), filepath.Join(dir, "decoder.int8.onnx"), filepath.Join(dir, "tokens.txt")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if m, fellBack := s.ASRModelFor("auto", "en"); m.ID != ASRModelCohere || fellBack {
		t.Errorf("Cohere present: got %s, fellBack %v", m.ID, fellBack)
	}
	if m, fellBack := s.ASRModelFor("parakeet-v2-en", "en"); m.ID != ASRModelParakeetV3 || !fellBack {
		t.Errorf("v2 missing: got %s, fellBack %v; want v3, true", m.ID, fellBack)
	}
	if m, _ := s.ASRModelFor("parakeet-v3", "en"); m.ID != ASRModelParakeetV3 {
		t.Errorf("opt-out: got %s, want v3", m.ID)
	}
}
