// Package diarize runs pyannote-style speaker diarization step by step,
// so the expensive steps (segmentation and speaker embeddings) can be
// computed once and cached while the cheap ones (clustering, merging,
// overlap handling) are re-run with different settings in milliseconds.
//
// It follows sherpa-onnx's OfflineSpeakerDiarization (v1.12.28,
// offline-speaker-diarization-pyannote-impl.h) step for step, with the same
// models, so its defaults reproduce what Tomoe's post-meeting pass produces
// today; what it adds is access to the intermediate results and the knobs
// sherpa-onnx keeps internal.
package diarize

import (
	"debug/elf"
	"debug/macho"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var (
	ortOnce sync.Once
	ortErr  error
)

// initORT loads the ONNX Runtime library sherpa-onnx already ships and
// links Tomoe against, so there is exactly one copy of it in the process.
func initORT() error {
	ortOnce.Do(func() {
		path, err := findOnnxRuntime()
		if err != nil {
			ortErr = err
			return
		}
		ort.SetSharedLibraryPath(path)
		if err := ort.InitializeEnvironment(); err != nil {
			ortErr = fmt.Errorf("initializing ONNX Runtime from %s: %w", path, err)
		}
	})
	return ortErr
}

// findOnnxRuntime locates libonnxruntime: $TOMOE_ONNXRUNTIME_LIB if set,
// else next to this executable, else in the directories this executable's
// own rpath/runpath lists (where the dynamic loader found the copy
// sherpa-onnx uses).
func findOnnxRuntime() (string, error) {
	if p := os.Getenv("TOMOE_ONNXRUNTIME_LIB"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	exeDir := filepath.Dir(exe)
	dirs := append([]string{exeDir}, executableRpaths(exe, exeDir)...)
	for _, dir := range dirs {
		for _, pattern := range []string{"libonnxruntime.dylib", "libonnxruntime.*.dylib", "libonnxruntime.so", "libonnxruntime.so.*"} {
			if matches, _ := filepath.Glob(filepath.Join(dir, pattern)); len(matches) > 0 {
				return matches[0], nil
			}
		}
	}
	return "", fmt.Errorf("libonnxruntime not found next to %s or in its rpaths (%s); set TOMOE_ONNXRUNTIME_LIB", exe, strings.Join(dirs, ", "))
}

// executableRpaths reads the library search paths baked into an executable
// (Mach-O LC_RPATH or ELF DT_RUNPATH/DT_RPATH), resolving the
// @executable_path, @loader_path and $ORIGIN placeholders.
func executableRpaths(exe, exeDir string) []string {
	resolve := func(p string) string {
		for _, prefix := range []string{"@executable_path", "@loader_path", "$ORIGIN", "${ORIGIN}"} {
			p = strings.ReplaceAll(p, prefix, exeDir)
		}
		return p
	}
	var out []string
	if f, err := macho.Open(exe); err == nil {
		defer f.Close()
		for _, l := range f.Loads {
			if r, ok := l.(*macho.Rpath); ok {
				out = append(out, resolve(r.Path))
			}
		}
		return out
	}
	if f, err := elf.Open(exe); err == nil {
		defer f.Close()
		for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
			vals, _ := f.DynString(tag)
			for _, v := range vals {
				for _, p := range strings.Split(v, ":") {
					if p != "" {
						out = append(out, resolve(p))
					}
				}
			}
		}
	}
	return out
}
