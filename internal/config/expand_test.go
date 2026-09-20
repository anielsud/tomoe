package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestExpandValue covers the substitution syntax, which exists so a secret
// can stay out of a file that gets committed or synced.
func TestExpandValue(t *testing.T) {
	t.Setenv("TOMOE_TEST_KEY", "apikey_from_env")
	t.Setenv("TOMOE_TEST_EMPTY", "")

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"a plain value is untouched", "sk-literal-value", "sk-literal-value"},
		{"an environment variable", "${TOMOE_TEST_KEY}", "apikey_from_env"},
		{"embedded in a longer value", "Bearer ${TOMOE_TEST_KEY}!", "Bearer apikey_from_env!"},
		{"twice in one value", "${TOMOE_TEST_KEY}/${TOMOE_TEST_KEY}", "apikey_from_env/apikey_from_env"},
		{"an unset variable becomes empty", "${TOMOE_TEST_MISSING}", ""},
		{"a set-but-empty variable takes the fallback", "${TOMOE_TEST_EMPTY:-fallback}", "fallback"},
		{"an unset variable takes the fallback", "${TOMOE_TEST_MISSING:-jev-1.13.0}", "jev-1.13.0"},
		{"a set variable ignores the fallback", "${TOMOE_TEST_KEY:-unused}", "apikey_from_env"},
		{"a command", "$(printf hello)", "hello"},
		{"command output is trimmed", "$(printf 'spaced  \n')", "spaced"},
		{"a command inside a longer value", "prefix-$(printf mid)-suffix", "prefix-mid-suffix"},
		{"both kinds together", "$(printf one)/${TOMOE_TEST_KEY}", "one/apikey_from_env"},
		// A dollar that is not a reference must survive: passwords contain them.
		{"a bare dollar", "pa$$word", "pa$word"},
		{"an escaped reference stays literal", "$${NOT_A_VAR}", "${NOT_A_VAR}"},
		{"a dollar with no braces", "cost is $5", "cost is $5"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandValue(tt.raw, "config.test")
			if err != nil {
				t.Fatalf("expandValue(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("expandValue(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestExpandValueReportsCommandFailure covers a command that does not work.
// Failing loudly beats yielding an empty credential that surfaces later as an
// authentication error.
func TestExpandValueReportsCommandFailure(t *testing.T) {
	_, err := expandValue("$(exit 3)", "config.transcription.hotwords_file")
	if err == nil {
		t.Fatal("a failing command expanded without error")
	}
	if !strings.Contains(err.Error(), "config.transcription.hotwords_file") {
		t.Errorf("error does not say which setting failed: %v", err)
	}
}

// TestExpandValueKeepsSecretsOutOfErrors covers the one thing this code must
// never do: a failure while resolving a credential must not quote it.
func TestExpandValueKeepsSecretsOutOfErrors(t *testing.T) {
	// The command prints a secret to stderr and then fails, which is exactly
	// what a confused credential helper does.
	_, err := expandValue("$(printf 'apikey_supersecret' >&2; exit 1)", "config.key")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("the error leaked the command's output: %v", err)
	}
}

// TestExpandConfigWalksTheWholeConfig covers expansion reaching nested
// structs and slices rather than a hand-picked list of fields.
func TestExpandConfigWalksTheWholeConfig(t *testing.T) {
	t.Setenv("TOMOE_TEST_HOME", "/home/imran")
	t.Setenv("TOMOE_TEST_KEY", "apikey_from_env")
	t.Setenv("TOMOE_TEST_DEVICE", "hw:0")

	cfg := &Config{
		Audio: AudioConfig{Device: "${TOMOE_TEST_DEVICE}"},
		Transcription: TranscriptionConfig{
			ModelPath:    "${TOMOE_TEST_HOME}/.local/share/tomoe/models",
			HotwordsFile: "$(printf /tmp/hotwords.txt)",
		},
		Multilingual: MultilingualConfig{
			Languages:   []string{"${TOMOE_TEST_MISSING:-en}", "bn"},
			DefaultLang: "${TOMOE_TEST_MISSING:-en}",
		},
	}

	if err := expandConfig(cfg); err != nil {
		t.Fatalf("expandConfig: %v", err)
	}

	checks := []struct{ got, want, where string }{
		{cfg.Audio.Device, "hw:0", "audio device"},
		{cfg.Transcription.ModelPath, "/home/imran/.local/share/tomoe/models", "model path"},
		{cfg.Transcription.HotwordsFile, "/tmp/hotwords.txt", "hotwords file (command)"},
		{cfg.Multilingual.Languages[0], "en", "languages[0] fallback"},
		{cfg.Multilingual.Languages[1], "bn", "languages[1] untouched"},
		{cfg.Multilingual.DefaultLang, "en", "default lang fallback"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.where, c.got, c.want)
		}
	}
}

// TestExpandConfigLeavesUntouchedValuesAlone covers the common case: nothing
// in the config uses expansion. Expansion must not mangle values that merely
// contain punctuation.
func TestExpandConfigLeavesUntouchedValuesAlone(t *testing.T) {
	cfg := DefaultConfig()
	// Add a couple of values that a user might have without $-expansion.
	cfg.Hotkey.Binding = "Super+Shift+S"
	cfg.Transcription.HotwordsFile = "/etc/tomoe/hotwords.txt"
	cfg.Multilingual.Languages = []string{"en", "bn"}

	before := *cfg
	beforeLangs := append([]string(nil), cfg.Multilingual.Languages...)

	if err := expandConfig(cfg); err != nil {
		t.Fatalf("expandConfig: %v", err)
	}

	if cfg.Hotkey.Binding != before.Hotkey.Binding {
		t.Errorf("hotkey binding changed: %q → %q", before.Hotkey.Binding, cfg.Hotkey.Binding)
	}
	if cfg.Transcription.HotwordsFile != before.Transcription.HotwordsFile {
		t.Errorf("hotwords file changed: %q → %q", before.Transcription.HotwordsFile, cfg.Transcription.HotwordsFile)
	}
	if !reflect.DeepEqual(cfg.Multilingual.Languages, beforeLangs) {
		t.Errorf("languages changed: %v → %v", beforeLangs, cfg.Multilingual.Languages)
	}
	// resolved must be empty when nothing was expanded.
	if len(cfg.resolved) != 0 {
		t.Errorf("resolved should be empty, got %d entries", len(cfg.resolved))
	}
}

// TestExpandConfigRecordsResolutions covers the resolved map: every value
// that changed gets a (path, expression, resolved) record so Save can
// unexpand it later.
func TestExpandConfigRecordsResolutions(t *testing.T) {
	t.Setenv("TOMOE_TEST_KEY", "apikey_from_env")

	cfg := &Config{
		Transcription: TranscriptionConfig{HotwordsFile: "${TOMOE_TEST_KEY}"},
	}
	if err := expandConfig(cfg); err != nil {
		t.Fatalf("expandConfig: %v", err)
	}
	if cfg.Transcription.HotwordsFile != "apikey_from_env" {
		t.Fatalf("hotwords_file = %q, want %q", cfg.Transcription.HotwordsFile, "apikey_from_env")
	}
	rv, ok := cfg.resolved["config.transcription.hotwords_file"]
	if !ok {
		t.Fatalf("no resolved entry for hotwords_file; keys: %v", keys(cfg.resolved))
	}
	if rv.expression != "${TOMOE_TEST_KEY}" {
		t.Errorf("expression = %q, want %q", rv.expression, "${TOMOE_TEST_KEY}")
	}
	if rv.resolved != "apikey_from_env" {
		t.Errorf("resolved = %q, want %q", rv.resolved, "apikey_from_env")
	}
}

// TestUnexpandForWriteRestoresSecrets covers the round-trip: values that
// were resolved from ${} / $() get their reference form back at write time,
// so Save never persists the resolved secret to disk.
func TestUnexpandForWriteRestoresSecrets(t *testing.T) {
	t.Setenv("TOMOE_TEST_KEY", "apikey_from_env")

	cfg := &Config{
		Transcription: TranscriptionConfig{HotwordsFile: "${TOMOE_TEST_KEY}"},
	}
	if err := expandConfig(cfg); err != nil {
		t.Fatalf("expandConfig: %v", err)
	}
	if cfg.Transcription.HotwordsFile != "apikey_from_env" {
		t.Fatalf("post-expand hotwords_file = %q", cfg.Transcription.HotwordsFile)
	}

	restore := cfg.unexpandForWrite()
	if cfg.Transcription.HotwordsFile != "${TOMOE_TEST_KEY}" {
		t.Errorf("during write, hotwords_file = %q, want %q", cfg.Transcription.HotwordsFile, "${TOMOE_TEST_KEY}")
	}
	restore()
	if cfg.Transcription.HotwordsFile != "apikey_from_env" {
		t.Errorf("after restore, hotwords_file = %q, want %q", cfg.Transcription.HotwordsFile, "apikey_from_env")
	}
}

// TestUnexpandForWriteLeavesEditedValuesAlone covers the case where the
// caller has changed a resolved value between Load and Save: that new value
// is what they want on disk, and it must NOT be reverted to the old
// reference.
func TestUnexpandForWriteLeavesEditedValuesAlone(t *testing.T) {
	t.Setenv("TOMOE_TEST_KEY", "apikey_from_env")

	cfg := &Config{
		Transcription: TranscriptionConfig{HotwordsFile: "${TOMOE_TEST_KEY}"},
	}
	if err := expandConfig(cfg); err != nil {
		t.Fatalf("expandConfig: %v", err)
	}
	// User replaced the resolved value with something new.
	cfg.Transcription.HotwordsFile = "/etc/tomoe/hotwords.txt"

	restore := cfg.unexpandForWrite()
	defer restore()

	if cfg.Transcription.HotwordsFile != "/etc/tomoe/hotwords.txt" {
		t.Errorf("edited value was reverted: %q", cfg.Transcription.HotwordsFile)
	}
}

// TestExpandValueTimesOut covers the timeout on a `$(...)` that hangs. The
// timeout is a load-bearing safety net: a locked keyring can otherwise block
// startup indefinitely.
func TestExpandValueTimesOut(t *testing.T) {
	saved := expandCommandTimeout
	expandCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { expandCommandTimeout = saved })

	start := time.Now()
	_, err := expandValue("$(sleep 5)", "config.slow")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error does not mention timeout: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout did not fire promptly: took %s", elapsed)
	}
}

// TestFieldNamePrefersTomlTag verifies the tag lookup uses `toml` (the tomoe
// convention), not `mapstructure` (CodeEagle's convention).
func TestFieldNamePrefersTomlTag(t *testing.T) {
	type example struct {
		Named   string `toml:"named_via_toml"`
		Unnamed string
		Comma   string `toml:"comma_name,inline"`
	}
	tt := reflect.TypeOf(example{})
	if got := fieldName(tt.Field(0)); got != "named_via_toml" {
		t.Errorf("Named: got %q, want %q", got, "named_via_toml")
	}
	if got := fieldName(tt.Field(1)); got != "Unnamed" {
		t.Errorf("Unnamed: got %q, want %q", got, "Unnamed")
	}
	if got := fieldName(tt.Field(2)); got != "comma_name" {
		t.Errorf("Comma: got %q, want %q", got, "comma_name")
	}
}

// TestWalkStringsHandlesMaps covers the reflection walk for map values,
// which are not addressable in place and must be copied and set.
func TestWalkStringsHandlesMaps(t *testing.T) {
	type withMap struct {
		StringMap map[string]string `toml:"string_map"`
	}
	t.Setenv("TOMOE_TEST_MAP_VAL", "resolved")
	w := &withMap{StringMap: map[string]string{"key": "${TOMOE_TEST_MAP_VAL}"}}

	err := walkStrings(reflect.ValueOf(w), "root", func(path, value string) (string, error) {
		return expandValue(value, path)
	})
	if err != nil {
		t.Fatalf("walkStrings: %v", err)
	}
	if got := w.StringMap["key"]; got != "resolved" {
		t.Errorf("map value = %q, want %q", got, "resolved")
	}
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
