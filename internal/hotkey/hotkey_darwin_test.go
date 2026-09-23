//go:build darwin

package hotkey

import "testing"

func TestVirtualKeyCode_Letters(t *testing.T) {
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		key := string(letter)
		if _, err := virtualKeyCode(key); err != nil {
			t.Errorf("virtualKeyCode(%q) error: %v", key, err)
		}
	}
}

func TestVirtualKeyCode_Digits(t *testing.T) {
	for i := 0; i <= 9; i++ {
		key := string(rune('0' + i))
		if _, err := virtualKeyCode(key); err != nil {
			t.Errorf("virtualKeyCode(%q) error: %v", key, err)
		}
	}
}

func TestVirtualKeyCode_SpecialKeys(t *testing.T) {
	keys := []string{"SPACE", "RETURN", "ENTER", "ESCAPE", "ESC", "TAB", "DELETE", "LEFT", "RIGHT", "UP", "DOWN"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if _, err := virtualKeyCode(key); err != nil {
				t.Errorf("virtualKeyCode(%q) error: %v", key, err)
			}
		})
	}
}

func TestVirtualKeyCode_ReturnAndEnterSameCode(t *testing.T) {
	ret, err := virtualKeyCode("RETURN")
	if err != nil {
		t.Fatal(err)
	}
	enter, err := virtualKeyCode("ENTER")
	if err != nil {
		t.Fatal(err)
	}
	if ret != enter {
		t.Errorf("RETURN keycode (%v) != ENTER keycode (%v)", ret, enter)
	}
}

func TestVirtualKeyCode_EscapeAndEscSameCode(t *testing.T) {
	escape, err := virtualKeyCode("ESCAPE")
	if err != nil {
		t.Fatal(err)
	}
	esc, err := virtualKeyCode("ESC")
	if err != nil {
		t.Fatal(err)
	}
	if escape != esc {
		t.Errorf("ESCAPE keycode (%v) != ESC keycode (%v)", escape, esc)
	}
}

func TestVirtualKeyCode_FunctionKeys(t *testing.T) {
	for i := 1; i <= 20; i++ {
		key := "F" + string(rune('0'+i%10))
		if i >= 10 {
			key = "F1" + string(rune('0'+i-10))
		}
		if i == 20 {
			key = "F20"
		}
		if _, err := virtualKeyCode(key); err != nil {
			t.Errorf("virtualKeyCode(%q) error: %v", key, err)
		}
	}
}

func TestVirtualKeyCode_Unsupported(t *testing.T) {
	unsupported := []string{"", "BACKSPACE", "HOME", "END", "PAGEUP", "PAGEDOWN", "a", "f1", "!!", "@"}
	for _, key := range unsupported {
		t.Run(key, func(t *testing.T) {
			if _, err := virtualKeyCode(key); err == nil {
				t.Errorf("virtualKeyCode(%q) should return error for unsupported key", key)
			}
		})
	}
}

func TestCarbonModifierFlags(t *testing.T) {
	tests := []struct {
		name string
		mods []string
	}{
		{"single Super", []string{"Super"}},
		{"single Ctrl", []string{"Ctrl"}},
		{"single Shift", []string{"Shift"}},
		{"single Alt", []string{"Alt"}},
		{"combo", []string{"Super", "Shift"}},
		{"none", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := carbonModifierFlags(tt.mods); err != nil {
				t.Errorf("carbonModifierFlags(%v) error: %v", tt.mods, err)
			}
		})
	}
}

func TestCarbonModifierFlags_DistinctBits(t *testing.T) {
	seen := make(map[uint32]string)
	for _, m := range []string{"Super", "Ctrl", "Shift", "Alt"} {
		flags, err := carbonModifierFlags([]string{m})
		if err != nil {
			t.Fatalf("carbonModifierFlags([%q]) error: %v", m, err)
		}
		key := uint32(flags)
		if prev, ok := seen[key]; ok {
			t.Errorf("modifier %q produced the same flag as %q (0x%x)", m, prev, key)
		}
		seen[key] = m
	}
}

func TestCarbonModifierFlags_Unsupported(t *testing.T) {
	if _, err := carbonModifierFlags([]string{"Meta"}); err == nil {
		t.Error("carbonModifierFlags([\"Meta\"]) should return error for unsupported modifier")
	}
}
