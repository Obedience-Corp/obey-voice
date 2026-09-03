package talkhotkey

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseCombo(t *testing.T) {
	tests := []struct {
		in   string
		want string
		mods Modifier
	}{
		{in: "alt+space", want: "alt+space", mods: ModAlt},
		{in: "ALT+SPACE", want: "alt+space", mods: ModAlt},
		{in: " option + spacebar ", want: "alt+space", mods: ModAlt},
		{in: "opt+ ", want: "alt+space", mods: ModAlt},
		{in: "⌥+space", want: "alt+space", mods: ModAlt},
		{in: "control+alt+t", want: "ctrl+alt+t", mods: ModCtrl | ModAlt},
		{in: "alt+ctrl+t", want: "ctrl+alt+t", mods: ModCtrl | ModAlt},
		{in: "⌃+⌥+t", want: "ctrl+alt+t", mods: ModCtrl | ModAlt},
		{in: "cmd+shift+enter", want: "shift+cmd+return", mods: ModShift | ModCmd},
		{in: "command+shift+return", want: "shift+cmd+return", mods: ModShift | ModCmd},
		{in: "super+esc", want: "cmd+escape", mods: ModCmd},
		{in: "ctrl+hyphen", want: "ctrl+minus", mods: ModCtrl},
		{in: "f13", want: "f13"},
		{in: "shift+f20", want: "shift+f20", mods: ModShift},
		{in: "alt+alt+space", want: "alt+space", mods: ModAlt},
		{in: "cmd+9", want: "cmd+9", mods: ModCmd},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			combo, err := ParseCombo(tc.in)
			if err != nil {
				t.Fatalf("ParseCombo(%q): %v", tc.in, err)
			}
			if got := combo.String(); got != tc.want {
				t.Fatalf("ParseCombo(%q).String() = %q, want %q", tc.in, got, tc.want)
			}
			if combo.Modifiers != tc.mods {
				t.Errorf("modifiers = %04b, want %04b", combo.Modifiers, tc.mods)
			}
			// The canonical spelling is what the helper is spawned with, so it
			// has to survive the round trip unchanged.
			again, err := ParseCombo(combo.String())
			if err != nil {
				t.Fatalf("reparse %q: %v", combo.String(), err)
			}
			if again != combo {
				t.Errorf("round trip changed %+v into %+v", combo, again)
			}
		})
	}
}

func TestParseComboRejections(t *testing.T) {
	tests := []struct {
		name string
		in   string
		says string
	}{
		{name: "empty", in: "", says: "empty"},
		{name: "bare space", in: "space", says: "ctrl, alt or cmd"},
		{name: "bare letter", in: "a", says: "ctrl, alt or cmd"},
		{name: "bare digit", in: "7", says: "ctrl, alt or cmd"},
		{name: "shift alone", in: "shift+a", says: "shift alone is not enough"},
		{name: "modifier only", in: "alt", says: "no key"},
		{name: "two keys", in: "alt+space+x", says: "want exactly one"},
		{name: "unknown key", in: "alt+unknownkey", says: "unknown key"},
		{name: "trailing plus", in: "alt+space+", says: "empty part"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			combo, err := ParseCombo(tc.in)
			if err == nil {
				t.Fatalf("ParseCombo(%q) = %+v, want an error", tc.in, combo)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("ParseCombo(%q) error %q does not name the rule (%q)", tc.in, err, tc.says)
			}
		})
	}
}

func TestComboDisplay(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "alt+space", want: "⌥Space"},
		{in: "ctrl+alt+t", want: "⌃⌥T"},
		{in: "f13", want: "F13"},
		{in: "cmd+shift+return", want: "⇧⌘Return"},
		{in: "ctrl+minus", want: "⌃-"},
		{in: "cmd+left", want: "⌘←"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			combo, err := ParseCombo(tc.in)
			if err != nil {
				t.Fatalf("ParseCombo(%q): %v", tc.in, err)
			}
			if got := combo.Display(); got != tc.want {
				t.Errorf("Display() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The key table is both the accepted set and the Carbon codes, so every key a
// user can parse must be registrable. A key that parsed without a code would
// only fail on macOS, at the moment someone pressed it.
func TestEveryAcceptedKeyHasACarbonKeyCode(t *testing.T) {
	var names []string
	for r := 'a'; r <= 'z'; r++ {
		names = append(names, string(r))
	}
	for d := range 10 {
		names = append(names, fmt.Sprint(d))
	}
	for n := 1; n <= 20; n++ {
		names = append(names, fmt.Sprintf("f%d", n))
	}
	names = append(names,
		"space", "return", "tab", "escape", "left", "right", "up", "down",
		"minus", "equal", "comma", "period", "slash", "semicolon", "quote",
		"backslash", "grave", "leftbracket", "rightbracket")

	if len(names) != len(keyCodes) {
		t.Errorf("documented %d keys, table holds %d", len(names), len(keyCodes))
	}
	seen := map[uint32]string{}
	for _, name := range names {
		combo, err := ParseCombo("ctrl+alt+" + name)
		if err != nil {
			t.Errorf("ParseCombo of key %q: %v", name, err)
			continue
		}
		code, ok := combo.CarbonKeyCode()
		if !ok {
			t.Errorf("key %q parses but has no Carbon key code", name)
			continue
		}
		if other, dup := seen[code]; dup {
			t.Errorf("keys %q and %q share Carbon key code %d", other, name, code)
		}
		seen[code] = name
	}
}

func TestCarbonModifiers(t *testing.T) {
	tests := []struct {
		in   string
		want uint32
	}{
		{in: "alt+space", want: 0x0800},
		{in: "ctrl+alt+t", want: 0x1800},
		{in: "cmd+shift+return", want: 0x0300},
		{in: "f13", want: 0},
		{in: "ctrl+alt+shift+cmd+t", want: 0x1B00},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			combo, err := ParseCombo(tc.in)
			if err != nil {
				t.Fatalf("ParseCombo(%q): %v", tc.in, err)
			}
			if got := combo.CarbonModifiers(); got != tc.want {
				t.Errorf("CarbonModifiers() = %#04x, want %#04x", got, tc.want)
			}
		})
	}
}

func TestDefaultComboParses(t *testing.T) {
	combo, err := ParseCombo(DefaultCombo)
	if err != nil {
		t.Fatalf("ParseCombo(DefaultCombo): %v", err)
	}
	if combo.String() != DefaultCombo {
		t.Errorf("DefaultCombo %q is not canonical (%q)", DefaultCombo, combo.String())
	}
}
