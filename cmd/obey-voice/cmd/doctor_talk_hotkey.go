package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// talkHotkeyProbeTimeout bounds the helper probe: registering a key is
// immediate, so anything slower is the helper failing to start.
const talkHotkeyProbeTimeout = 3 * time.Second

// probeTalkHotkey runs the helper in probe mode and returns the first event it
// prints. A seam so the doctor tests never register a real key on the host.
var probeTalkHotkey = func(ctx context.Context, combo talkhotkey.Combo) (talkhotkey.Event, error) {
	self, err := os.Executable()
	if err != nil {
		return talkhotkey.Event{}, err
	}
	out, err := exec.CommandContext(ctx, self, "talk-hotkey", "--probe", "--combo", combo.String()).Output()
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if ev, ok := talkhotkey.ParseEvent(scanner.Bytes()); ok {
			return ev, nil
		}
	}
	if err != nil {
		return talkhotkey.Event{}, err
	}
	return talkhotkey.Event{}, errors.New("the helper printed no event")
}

// talkHotkeyDiags reports whether the talk key can be held on this machine
// right now: first the one collision registration cannot see (the Mac app on
// the same combo), then a real registration, released immediately. Silent
// where the key is off or the platform has no helper, matching the rest of
// doctor. A combo that does not parse is already reported by the config layer.
func talkHotkeyDiags(ctx context.Context, cfg *config.Config) []config.Diagnostic {
	if cfg == nil || !cfg.TalkHotkeyEnabled || !talkhotkey.Supported() {
		return nil
	}
	combo, err := config.TalkHotkeyComboOf(cfg)
	if err != nil {
		return nil
	}
	label := combo.Display() + " (" + string(config.TalkHotkeyModeOf(cfg)) + ")"
	if owner, ok := talkhotkey.OtherOwner(combo); ok {
		return []config.Diagnostic{{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + ": " + owner + " is running and answers the same key, so the TUI leaves it to the app",
			Remediation: "set talk_hotkey to a combo the app does not use, e.g. ctrl+alt+space, or quit the app while using the TUI",
		}}
	}

	ctx, cancel := context.WithTimeout(ctx, talkHotkeyProbeTimeout)
	defer cancel()
	ev, err := probeTalkHotkey(ctx, combo)
	switch {
	case err != nil && ev.Kind == "":
		return []config.Diagnostic{{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      fmt.Sprintf("talk hotkey %s could not be probed: %v", label, err),
			Remediation: "the conversation falls back to Ctrl+G; rebuild with cgo on macOS, or set talk_hotkey_enabled: false",
		}}
	case ev.Kind == talkhotkey.KindRegistered:
		return []config.Diagnostic{{
			Name:     "talk-hotkey",
			Severity: config.SeverityOK,
			Detail:   "talk hotkey " + label + " registers (macOS also lets other apps hold the same key; Raycast and Spotlight default to modifier+space)",
		}}
	case ev.Conflict():
		return []config.Diagnostic{{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + " is owned by another app (the Obey Voice Mac app, Raycast, or a running obey-voice conversation)",
			Remediation: "quit the other owner or set talk_hotkey to a different combo, e.g. ctrl+alt+space",
		}}
	default:
		return []config.Diagnostic{{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + " failed to register: " + ev.Detail,
			Remediation: "set talk_hotkey to a different combo, or talk_hotkey_enabled: false to use Ctrl+G only",
		}}
	}
}
