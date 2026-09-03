package talkhotkey

import (
	"slices"
	"testing"
)

// Every key the parser accepts has to have a Windows virtual key, or a combo
// would parse on Windows and then register nothing.
func TestEveryParsedKeyHasAWindowsVirtualKey(t *testing.T) {
	missing := make([]string, 0)
	for key := range keyCodes {
		if _, ok := (Combo{Key: key}).WindowsVirtualKey(); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		t.Fatalf("keys with no Windows virtual key: %v", missing)
	}
	if len(windowsKeys) != len(keyCodes) {
		t.Fatalf("windowsKeys has %d entries, keyCodes has %d; the two key sets must match", len(windowsKeys), len(keyCodes))
	}
}

func TestWindowsModifiersMapEveryBit(t *testing.T) {
	for name, tc := range map[string]struct {
		spelling string
		want     uint32
	}{
		"alt":            {"alt+space", winModAlt},
		"ctrl+alt":       {"ctrl+alt+space", winModControl | winModAlt},
		"cmd is the win": {"cmd+shift+t", winModWin | winModShift},
		"bare function":  {"f13", 0},
	} {
		t.Run(name, func(t *testing.T) {
			combo, err := ParseCombo(tc.spelling)
			if err != nil {
				t.Fatalf("ParseCombo(%q): %v", tc.spelling, err)
			}
			if got := combo.WindowsModifiers(); got != tc.want {
				t.Fatalf("WindowsModifiers() = %#x, want %#x", got, tc.want)
			}
		})
	}
}

// The default combo differs by OS because Alt+Space is the window menu on
// Windows: registering it would take a shortcut every Win32 app answers.
func TestDefaultComboForPlatform(t *testing.T) {
	if got := DefaultComboFor("windows"); got != "ctrl+alt+space" {
		t.Fatalf("DefaultComboFor(windows) = %q, want ctrl+alt+space", got)
	}
	for _, goos := range []string{"darwin", "linux", ""} {
		if got := DefaultComboFor(goos); got != DefaultCombo {
			t.Fatalf("DefaultComboFor(%q) = %q, want %q", goos, got, DefaultCombo)
		}
	}
	for _, spelling := range []string{DefaultComboFor("windows"), DefaultComboFor("darwin")} {
		if _, err := ParseCombo(spelling); err != nil {
			t.Fatalf("the platform default %q does not parse: %v", spelling, err)
		}
	}
}
