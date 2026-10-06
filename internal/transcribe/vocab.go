package transcribe

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// bpeVocabFor returns a BPE vocabulary for the model whose tokens.txt is at
// tokensPath, writing it next to tokens.txt (or in the user cache directory
// if that isn't writable) the first time. sherpa-onnx needs one to turn
// hotwords into the model's tokens: without it every hotword "failed to
// encode" and was skipped. Parakeet archives ship only tokens.txt ("piece
// id"), so the vocabulary is built from it, each piece scored by its
// negated ID (lower IDs are the more frequent merges).
func bpeVocabFor(tokensPath string) (string, error) {
	out := filepath.Join(filepath.Dir(tokensPath), "bpe.vocab")
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, nil
	}
	in, err := os.Open(tokensPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	var b strings.Builder
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		i := strings.LastIndexByte(line, ' ')
		if i <= 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\t-%s\n", line[:i], line[i+1:])
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err == nil {
		return out, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "tomoe", filepath.Base(filepath.Dir(tokensPath)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out = filepath.Join(dir, "bpe.vocab")
	return out, os.WriteFile(out, []byte(b.String()), 0o644)
}
