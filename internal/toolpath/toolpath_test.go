package toolpath

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFindIn_FirstExecutableWins(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(b, "tool"), 0o755)
	got, err := findIn("tool", []string{a, b})
	if err != nil || got != filepath.Join(b, "tool") {
		t.Fatalf("findIn = %q, %v; want %q", got, err, filepath.Join(b, "tool"))
	}
}

func TestFindIn_SkipsNonExecutableAndDirectories(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(a, "tool"), 0o644)
	if err := os.Mkdir(filepath.Join(b, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(c, "tool"), 0o755)
	got, err := findIn("tool", []string{a, b, c})
	if err != nil || got != filepath.Join(c, "tool") {
		t.Fatalf("findIn = %q, %v; want %q", got, err, filepath.Join(c, "tool"))
	}
}

func TestFindIn_NotFound(t *testing.T) {
	if _, err := findIn("tool", []string{t.TempDir()}); err == nil {
		t.Fatal("findIn found a tool that doesn't exist")
	}
}

func TestFind_FallsBackWhenNotOnPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tomoe-toolpath-test"), 0o755)
	t.Setenv("PATH", "/usr/bin:/bin") // launchd's PATH for a Dock-launched app
	saved := fallbackDirs
	fallbackDirs = []string{dir}
	t.Cleanup(func() { fallbackDirs = saved })

	got, err := Find("tomoe-toolpath-test")
	if err != nil || got != filepath.Join(dir, "tomoe-toolpath-test") {
		t.Fatalf("Find = %q, %v", got, err)
	}
}
