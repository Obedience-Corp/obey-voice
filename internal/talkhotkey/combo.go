// Package talkhotkey owns the system-wide talk hotkey: how a combo is spelled,
// the line protocol the macOS helper process speaks, the state machine that
// turns key events into turn boundaries, and the parent side that runs the
// helper.
//
// Everything here is cgo-free and builds on every OS so the TUI, config
// validation and tests never depend on Carbon. Only carbon_darwin.go touches
// the platform.
package talkhotkey

import (
	"errors"
	"fmt"
	"strings"
)

// Modifier is a set of chord modifiers, not a single one — Combo stores the
// whole set in one field.
type Modifier uint8

const (
	ModCtrl Modifier = 1 << iota
	ModAlt
	ModShift
	ModCmd
)

// Combo is one hotkey: a set of modifiers plus exactly one key. Key is always
// canonical lowercase (see keyCodes for the accepted names), so two Combos
// spelled differently by the user compare equal.
type Combo struct {
	Modifiers Modifier
	Key       string
}

// DefaultCombo is the shipped default. Option+Space is free on a stock macOS
// (Spotlight owns Cmd+Space) and reachable with one hand.
const DefaultCombo = "alt+space"

var modifierAliases = map[string]Modifier{
	"ctrl": ModCtrl, "control": ModCtrl, "⌃": ModCtrl,
	"alt": ModAlt, "option": ModAlt, "opt": ModAlt, "⌥": ModAlt,
	"shift": ModShift, "⇧": ModShift,
	"cmd": ModCmd, "command": ModCmd, "super": ModCmd, "⌘": ModCmd,
}

var keyAliases = map[string]string{
	"spacebar": "space",
	"enter":    "return",
	"esc":      "escape",
	"hyphen":   "minus",
}

// keyCodes is both the accepted key set and the US-layout virtual key codes
// (kVK_*) Carbon registers against. One table, so a key that parses always has
// a code to register — there is no second list to keep in step.
var keyCodes = map[string]uint32{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
	"b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
	"o": 31, "u": 32, "i": 34, "p": 35, "l": 37, "j": 38, "k": 40,
	"n": 45, "m": 46,

	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23,
	"9": 25, "7": 26, "8": 28, "0": 29,

	"equal": 24, "minus": 27, "rightbracket": 30, "leftbracket": 33,
	"quote": 39, "semicolon": 41, "backslash": 42, "comma": 43,
	"slash": 44, "period": 47, "grave": 50,

	"return": 36, "tab": 48, "space": 49, "escape": 53,
	"left": 123, "right": 124, "down": 125, "up": 126,

	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97, "f7": 98,
	"f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111, "f13": 105,
	"f14": 107, "f15": 113, "f16": 106, "f17": 64, "f18": 79, "f19": 80,
	"f20": 90,
}

// Carbon modifier bits (Events.h). Named here rather than in the cgo file so
// CarbonModifiers stays testable on every OS.
const (
	carbonCmdKey     uint32 = 0x0100
	carbonShiftKey   uint32 = 0x0200
	carbonOptionKey  uint32 = 0x0800
	carbonControlKey uint32 = 0x1000
)

// ParseCombo reads a combo spelled by a human: "alt+space", "Ctrl+Alt+T",
// "f13", "⌘+⇧+return". Glyphs are accepted as parts, but every part is still
// separated by "+" — this parses config values and CLI flags, not keystrokes.
func ParseCombo(s string) (Combo, error) {
	if s == "" {
		return Combo{}, errors.New("hotkey is empty; want modifiers plus a key, like alt+space")
	}
	var combo Combo
	var keys []string
	for part := range strings.SplitSeq(s, "+") {
		token := resolveToken(part)
		if token == "" {
			return Combo{}, fmt.Errorf("hotkey %q has an empty part", s)
		}
		if mod, ok := modifierAliases[token]; ok {
			combo.Modifiers |= mod
			continue
		}
		keys = append(keys, token)
	}
	switch len(keys) {
	case 1:
	case 0:
		return Combo{}, fmt.Errorf("hotkey %q has modifiers but no key", s)
	default:
		return Combo{}, fmt.Errorf("hotkey %q names %d keys (%s); want exactly one", s, len(keys), strings.Join(keys, ", "))
	}
	combo.Key = keys[0]
	if _, ok := keyCodes[combo.Key]; !ok {
		return Combo{}, fmt.Errorf("hotkey %q: unknown key %q; want a letter, a digit, f1-f20, or a named key such as space, return, tab, escape, minus or comma", s, combo.Key)
	}
	if err := combo.validate(); err != nil {
		return Combo{}, err
	}
	return combo, nil
}

// resolveToken canonicalizes one "+"-separated part. A literal space is
// matched before trimming, because " " is a legitimate way to spell the space
// key ("alt+ ") and trimming would erase it.
func resolveToken(raw string) string {
	lower := strings.ToLower(raw)
	if lower == " " {
		return "space"
	}
	token := strings.TrimSpace(lower)
	if canonical, ok := keyAliases[token]; ok {
		return canonical
	}
	return token
}

// validate enforces the rule the OS enforces anyway: a hotkey that swallows a
// bare letter, digit or space would eat that key in every app. Shift does not
// count — Shift+A is still a typed character.
func (c Combo) validate() error {
	if c.Modifiers&(ModCtrl|ModAlt|ModCmd) != 0 {
		return nil
	}
	if isFunctionKey(c.Key) {
		return nil
	}
	return fmt.Errorf("hotkey %q needs at least one of ctrl, alt or cmd, or a bare function key f1-f20; shift alone is not enough", c.String())
}

func isFunctionKey(key string) bool {
	if len(key) < 2 || key[0] != 'f' {
		return false
	}
	_, ok := keyCodes[key]
	return ok && key[1] >= '0' && key[1] <= '9'
}

// String is the canonical spelling, and the one the helper is spawned with:
// modifiers in ctrl, alt, shift, cmd order, then the key.
func (c Combo) String() string {
	var b strings.Builder
	for _, m := range []struct {
		bit  Modifier
		name string
	}{
		{ModCtrl, "ctrl"}, {ModAlt, "alt"}, {ModShift, "shift"}, {ModCmd, "cmd"},
	} {
		if c.Modifiers&m.bit != 0 {
			b.WriteString(m.name)
			b.WriteByte('+')
		}
	}
	b.WriteString(c.Key)
	return b.String()
}

var keyTitles = map[string]string{
	"space": "Space", "return": "Return", "tab": "Tab", "escape": "Esc",
	"left": "←", "right": "→", "up": "↑", "down": "↓",
	"minus": "-", "equal": "=", "comma": ",", "period": ".", "slash": "/",
	"semicolon": ";", "quote": "'", "backslash": "\\", "grave": "`",
	"leftbracket": "[", "rightbracket": "]",
}

// Display is the macOS spelling for a settings row or a hint line: glyphs in
// Apple's order (⌃⌥⇧⌘) with no separator, then the key.
func (c Combo) Display() string {
	var b strings.Builder
	for _, m := range []struct {
		bit   Modifier
		glyph string
	}{
		{ModCtrl, "⌃"}, {ModAlt, "⌥"}, {ModShift, "⇧"}, {ModCmd, "⌘"},
	} {
		if c.Modifiers&m.bit != 0 {
			b.WriteString(m.glyph)
		}
	}
	if title, ok := keyTitles[c.Key]; ok {
		b.WriteString(title)
	} else {
		b.WriteString(strings.ToUpper(c.Key))
	}
	return b.String()
}

// CarbonKeyCode reports the US-layout virtual key code RegisterEventHotKey
// takes. False means the key is not one this build can register.
func (c Combo) CarbonKeyCode() (uint32, bool) {
	code, ok := keyCodes[c.Key]
	return code, ok
}

// CarbonModifiers is the modifier mask RegisterEventHotKey takes.
func (c Combo) CarbonModifiers() uint32 {
	var mask uint32
	if c.Modifiers&ModCtrl != 0 {
		mask |= carbonControlKey
	}
	if c.Modifiers&ModAlt != 0 {
		mask |= carbonOptionKey
	}
	if c.Modifiers&ModShift != 0 {
		mask |= carbonShiftKey
	}
	if c.Modifiers&ModCmd != 0 {
		mask |= carbonCmdKey
	}
	return mask
}
