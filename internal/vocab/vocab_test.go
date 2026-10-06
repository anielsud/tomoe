package vocab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFromSessionsAndList(t *testing.T) {
	dir := t.TempDir()
	write := func(id, body string) {
		if err := os.MkdirAll(filepath.Join(dir, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id, "session.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", `{"segments":[{"speaker":"Person 1 (Priya Desai)","source":"monitor"},{"speaker":"Person 2 (Jordan Lee…)","source":"monitor"},{"speaker":"Person 3","source":"monitor"},{"speaker":"You","source":"mic"}]}`)
	write("b", `{"segments":[{"speaker":"Person 4 (Priya Desai)","source":"monitor"},{"speaker":"Person 5 (ake control Annotate)","source":"monitor"},{"speaker":"Al Kim","source":"monitor"}]}`)
	list := filepath.Join(dir, "vocabulary.txt")
	if err := os.WriteFile(list, []byte("# products\nOpal\nSOC 2  # standard\n\nopal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(Build(Sources{SessionsDir: dir, Files: []string{list}}), "|")
	want := "Priya Desai|Priya|Al Kim|Jordan Lee|Jordan|Opal|SOC 2"
	if got != want {
		t.Errorf("Build =\n%s\nwant\n%s", got, want)
	}
}
