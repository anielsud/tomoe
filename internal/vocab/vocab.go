// Package vocab builds the vocabulary transcription is steered towards
// (sherpa-onnx hotwords): words the speech model doesn't know or mishears,
// mostly people's names and domain terms. See
// docs/transcription-accuracy-research.md.
package vocab

import (
	"bufio"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// Sources says where vocabulary comes from. Zero values turn a source off.
type Sources struct {
	// Files are word lists, one word or phrase per line ("#" comments).
	Files []string
	// SessionsDir holds saved sessions whose speaker names (named by the
	// meeting window or renamed by the user) are people the user meets;
	// sessions older than Since are ignored (0: all).
	SessionsDir string
	Since       time.Duration
	// Before, if set, ignores sessions saved at or after it (replaying a
	// meeting with only what was known before it).
	Before time.Time
	// AccountName adds the computer account's full name: the host's own
	// name, which the meeting window labels "You" and so never names.
	AccountName bool
}

// Build collects the vocabulary from src: each name in full and its first
// name, then the word lists, without duplicates (case-insensitive).
func Build(src Sources) []string {
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		w = strings.Join(strings.Fields(w), " ")
		if w == "" || seen[strings.ToLower(w)] {
			return
		}
		seen[strings.ToLower(w)] = true
		out = append(out, w)
	}
	addName := func(n string) {
		add(n)
		if f := strings.Fields(n); len(f) > 1 && len([]rune(f[0])) >= minFirstName {
			add(f[0])
		}
	}
	if src.AccountName {
		if u, err := user.Current(); err == nil && plausibleName(u.Name) {
			addName(u.Name)
		}
	}
	if src.SessionsDir != "" {
		for _, n := range sessionNames(src.SessionsDir, src.Since, src.Before) {
			addName(n)
		}
	}
	for _, f := range src.Files {
		for _, w := range readList(f) {
			add(w)
		}
	}
	return out
}

// minFirstName is the shortest first name added on its own: shorter ones
// ("Al", "Ed", "Sal") are too often ordinary words or parts of them.
const minFirstName = 4

// Write saves words as a hotwords file (one per line), returning whether
// there was anything to write.
func Write(words []string, path string) (bool, error) {
	if len(words) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(strings.Join(words, "\n")+"\n"), 0o644)
}

func readList(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

var hintedLabel = regexp.MustCompile(`^Person \d+ \((.+)\)$`)

// sessionNames are the people named in saved sessions' speaker labels,
// most sessions first.
func sessionNames(dir string, since time.Duration, before time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	count := map[string]int{}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name(), "session.json")
		fi, err := os.Stat(path)
		if err != nil || (since > 0 && time.Since(fi.ModTime()) > since) || (!before.IsZero() && !fi.ModTime().Before(before)) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s struct {
			Segments []struct {
				Speaker string `json:"speaker"`
				Source  string `json:"source"`
			} `json:"segments"`
		}
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		inSession := map[string]bool{}
		for _, seg := range s.Segments {
			if seg.Source == "mic" {
				continue
			}
			name := seg.Speaker
			if m := hintedLabel.FindStringSubmatch(name); m != nil {
				name = m[1]
			} else if strings.HasPrefix(name, "Person ") {
				continue
			}
			if name = cleanName(name); plausibleName(name) {
				inSession[name] = true
			}
		}
		for n := range inSession {
			count[n]++
		}
	}
	names := make([]string, 0, len(count))
	for n := range count {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if count[names[i]] != count[names[j]] {
			return count[names[i]] > count[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// cleanName drops what a name label carries besides the name: Teams'
// truncation mark, a provisional "?", quotes and stray punctuation.
func cleanName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.TrimRight(n, ".…?'\"’ ")
	n = strings.TrimLeft(n, "'\"‘“ ")
	return n
}

// plausibleName accepts one to four words that start with a capital and
// are mostly letters: a person's name, not a label scrap.
func plausibleName(n string) bool {
	f := strings.Fields(n)
	if len(f) == 0 || len(f) > 4 {
		return false
	}
	for _, w := range f {
		r := []rune(w)
		if !unicode.IsUpper(r[0]) {
			return false
		}
		letters := 0
		for _, c := range r {
			if unicode.IsLetter(c) || c == '-' || c == '\'' {
				letters++
			}
		}
		if letters < len(r) {
			return false
		}
	}
	return len([]rune(strings.ReplaceAll(n, " ", ""))) >= 3
}

// Config is what the app reads from config.toml to build the vocabulary.
type Config struct {
	HotwordsFile   string // transcription.hotwords_file, a word list
	VocabularyFile string // transcription.vocabulary_file; "" = DefaultFile if it exists
	Names          bool   // transcription.vocabulary_names
	SessionsDir    string
}

// DefaultFile is the vocabulary list read when none is configured:
// vocabulary.txt next to config.toml.
func DefaultFile(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "vocabulary.txt")
}

// HotwordsFile builds the vocabulary c describes and writes it to out,
// returning out, or "" when there's nothing to steer towards (transcription
// then runs exactly as without hotwords).
func HotwordsFile(c Config, out string) string {
	src := Sources{}
	if c.HotwordsFile != "" {
		src.Files = append(src.Files, c.HotwordsFile)
	}
	if c.VocabularyFile != "" {
		src.Files = append(src.Files, c.VocabularyFile)
	}
	if c.Names {
		src.SessionsDir, src.Since, src.AccountName = c.SessionsDir, namesWindow, true
	}
	if ok, err := Write(Build(src), out); err != nil || !ok {
		return ""
	}
	return out
}

// namesWindow is how far back saved sessions supply names.
const namesWindow = 120 * 24 * time.Hour

// ForApp builds the vocabulary cfg's settings describe into Tomoe's cache
// directory and returns the hotwords file for the transcription engine,
// or "" when there's none.
func ForApp(cfg *config.Config) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return HotwordsFile(Config{
		HotwordsFile:   cfg.Transcription.HotwordsFile,
		VocabularyFile: cfg.VocabularyPath(),
		Names:          cfg.Transcription.VocabularyNames,
		SessionsDir:    config.SessionDir(),
	}, filepath.Join(dir, "tomoe", "hotwords.txt"))
}
