package config

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

// TalkHotkeyModeOf normalizes cfg's talk hotkey mode. A value that does not
// parse falls back to the default rather than failing here — and the default
// is hold, the conservative direction: a typo can only ever cost a
// press-and-hold, never leave the microphone open. ValidateTalkHotkey is what
// refuses the typo out loud. Same split as BargeInSpeakersOf /
// ValidateBargeInSpeakers.
func TalkHotkeyModeOf(cfg *Config) talkhotkey.Mode {
	if cfg == nil {
		return talkhotkey.DefaultMode
	}
	mode, err := talkhotkey.ParseMode(cfg.TalkHotkeyMode)
	if err != nil {
		return talkhotkey.DefaultMode
	}
	return mode
}

// TalkHotkeyComboOf parses cfg's combo. Unlike the mode there is no safe
// fallback: silently registering a different key than the one in config.yaml
// would leave the user pressing a key nothing listens to, so a bad value is an
// error the caller has to show.
func TalkHotkeyComboOf(cfg *Config) (talkhotkey.Combo, error) {
	spelling := talkhotkey.DefaultCombo
	if cfg != nil {
		spelling = cmp.Or(strings.TrimSpace(cfg.TalkHotkey), talkhotkey.DefaultCombo)
	}
	combo, err := talkhotkey.ParseCombo(spelling)
	if err != nil {
		return talkhotkey.Combo{}, fmt.Errorf("talk_hotkey: %w", err)
	}
	return combo, nil
}

// ValidateTalkHotkey rejects an unusable mode or combo, naming the key at
// fault. It validates even when the hotkey is switched off: the value is still
// wrong, and finding that out the first time you enable the key — with no idea
// which of the two settings is at fault — is worse than being told now.
func ValidateTalkHotkey(cfg *Config) error {
	raw := ""
	if cfg != nil {
		raw = cfg.TalkHotkeyMode
	}
	if _, err := talkhotkey.ParseMode(raw); err != nil {
		return fmt.Errorf("talk_hotkey_mode: %w", err)
	}
	_, err := TalkHotkeyComboOf(cfg)
	return err
}
