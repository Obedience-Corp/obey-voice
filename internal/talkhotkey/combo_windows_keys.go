package talkhotkey

// The Windows half of the combo grammar. Untagged on purpose: the mapping is
// a table, and a table that only compiles on one OS is a table nobody's tests
// ever check. hotkey_windows.go is the only file that calls into user32.

// RegisterHotKey modifier bits (winuser.h MOD_*). ModCmd is the Windows key,
// which is the same physical position as Command on a Mac keyboard.
const (
	winModAlt     uint32 = 0x0001
	winModControl uint32 = 0x0002
	winModShift   uint32 = 0x0004
	winModWin     uint32 = 0x0008
)

// windowsKeys is the US-layout virtual key code for every key ParseCombo
// accepts. It has to cover all of keyCodes: a combo that parses on Windows and
// then has no VK would register nothing and report no reason.
var windowsKeys = map[string]uint32{
	"a": 0x41, "b": 0x42, "c": 0x43, "d": 0x44, "e": 0x45, "f": 0x46, "g": 0x47,
	"h": 0x48, "i": 0x49, "j": 0x4A, "k": 0x4B, "l": 0x4C, "m": 0x4D, "n": 0x4E,
	"o": 0x4F, "p": 0x50, "q": 0x51, "r": 0x52, "s": 0x53, "t": 0x54, "u": 0x55,
	"v": 0x56, "w": 0x57, "x": 0x58, "y": 0x59, "z": 0x5A,

	"0": 0x30, "1": 0x31, "2": 0x32, "3": 0x33, "4": 0x34,
	"5": 0x35, "6": 0x36, "7": 0x37, "8": 0x38, "9": 0x39,

	"space": 0x20, "return": 0x0D, "tab": 0x09, "escape": 0x1B,
	"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,

	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73, "f5": 0x74, "f6": 0x75,
	"f7": 0x76, "f8": 0x77, "f9": 0x78, "f10": 0x79, "f11": 0x7A, "f12": 0x7B,
	"f13": 0x7C, "f14": 0x7D, "f15": 0x7E, "f16": 0x7F, "f17": 0x80,
	"f18": 0x81, "f19": 0x82, "f20": 0x83,

	// OEM keys are layout-dependent by definition; these are the US names,
	// the same assumption keyCodes already makes for macOS.
	"minus": 0xBD, "equal": 0xBB, "comma": 0xBC, "period": 0xBE, "slash": 0xBF,
	"semicolon": 0xBA, "quote": 0xDE, "backslash": 0xDC, "grave": 0xC0,
	"leftbracket": 0xDB, "rightbracket": 0xDD,
}

// WindowsVirtualKey reports the virtual key code RegisterHotKey takes. False
// means the key is not one this build can register.
func (c Combo) WindowsVirtualKey() (uint32, bool) {
	vk, ok := windowsKeys[c.Key]
	return vk, ok
}

// WindowsModifiers is the modifier mask RegisterHotKey takes, without
// MOD_NOREPEAT — the caller adds that, because it is a policy about key
// repeat rather than part of the combo.
func (c Combo) WindowsModifiers() uint32 {
	var mask uint32
	if c.Modifiers&ModCtrl != 0 {
		mask |= winModControl
	}
	if c.Modifiers&ModAlt != 0 {
		mask |= winModAlt
	}
	if c.Modifiers&ModShift != 0 {
		mask |= winModShift
	}
	if c.Modifiers&ModCmd != 0 {
		mask |= winModWin
	}
	return mask
}
