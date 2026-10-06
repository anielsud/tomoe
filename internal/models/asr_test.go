package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveASRModel(t *testing.T) {
	for _, c := range []struct{ setting, lang, want string }{
		{"auto", "en", ASRModelParakeetV2En},
		{"", "", ASRModelParakeetV2En},
		{"auto", "bn", ASRModelParakeetV3},
		{"auto", "de", ASRModelParakeetV3},
		{"parakeet-v3", "en", ASRModelParakeetV3},
		{"parakeet-v2-en", "en", ASRModelParakeetV2En},
		{"nonsense", "en", ASRModelParakeetV2En},
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
	if got := ASRModelsNeeded("auto", []string{"en", "bn"}); len(got) != 1 || got[0].ID != ASRModelParakeetV2En {
		t.Errorf("auto with English needs v2, got %v", got)
	}
}

func TestASRModelForFallsBackWhenMissing(t *testing.T) {
	s := &Status{ModelDir: t.TempDir()}
	if m, fellBack := s.ASRModelFor("auto", "en"); m.ID != ASRModelParakeetV3 || !fellBack {
		t.Errorf("v2 missing: got %s, fellBack %v; want v3, true", m.ID, fellBack)
	}
	v2, _ := ASRModelByID(ASRModelParakeetV2En)
	e, d, j, tok := s.ASRModelFiles(v2)
	for _, f := range []string{e, d, j, tok} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if m, fellBack := s.ASRModelFor("auto", "en"); m.ID != ASRModelParakeetV2En || fellBack {
		t.Errorf("v2 present: got %s, fellBack %v", m.ID, fellBack)
	}
	if m, _ := s.ASRModelFor("parakeet-v3", "en"); m.ID != ASRModelParakeetV3 {
		t.Errorf("opt-out: got %s, want v3", m.ID)
	}
}
