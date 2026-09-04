package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

// nativeTalkHotkey reports whether this OS lets a process claim a key for
// itself. A seam so the control-endpoint row can be tested from any host.
var nativeTalkHotkey = talkhotkey.Supported

// talkHotkeyOS is the platform this copy is written for. macOS shares a hot key
// between every app that registers it; Windows gives it to one process. The
// rows say different things because the facts are different. A seam so both
// branches are tested from one host.
var talkHotkeyOS = runtime.GOOS

// talkHotkeyDiags reports how the talk key can be reached on this machine right
// now. Where a process may claim a key that is a real registration, released
// immediately, preceded by the one collision registration cannot see. The
// control endpoint is reported alongside it, and alone where nothing may claim
// a key — the working path there rather than a degraded one. Silent where the
// key is off; a combo that does not parse is already the config layer's row.
func talkHotkeyDiags(ctx context.Context, cfg *config.Config) []config.Diagnostic {
	if cfg == nil || !cfg.TalkHotkeyEnabled {
		return nil
	}
	combo, err := config.TalkHotkeyComboOf(cfg)
	if err != nil {
		return nil
	}
	native := nativeTalkHotkey()
	// The endpoint row goes out wherever the key is on: it is the whole talk
	// key where nothing may claim one, and a second way in where a native key
	// exists. Either way doctor names the address, which is what the docs say
	// it does.
	if !native {
		return []config.Diagnostic{talkControlDiag(native)}
	}
	return []config.Diagnostic{nativeTalkHotkeyDiag(ctx, cfg, combo), talkControlDiag(native)}
}

// nativeTalkHotkeyDiag is the row for the key this process holds itself: first
// the one collision registration cannot see, then a real registration.
func nativeTalkHotkeyDiag(ctx context.Context, cfg *config.Config, combo talkhotkey.Combo) config.Diagnostic {
	label := combo.Display() + " (" + string(config.TalkHotkeyModeOf(cfg)) + ")"
	if owner, ok := talkhotkey.OtherOwner(combo); ok {
		return config.Diagnostic{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + ": " + owner + " is running and answers the same key, so the TUI leaves it to the app",
			Remediation: "set talk_hotkey to a combo the app does not use, e.g. ctrl+alt+space, or quit the app while using the TUI",
		}
	}
	return probedTalkHotkeyDiag(ctx, combo, label)
}

// probedTalkHotkeyDiag registers the combo for an instant and reports what
// happened, which is the only way to know it is actually free.
func probedTalkHotkeyDiag(ctx context.Context, combo talkhotkey.Combo, label string) config.Diagnostic {
	ctx, cancel := context.WithTimeout(ctx, talkHotkeyProbeTimeout)
	defer cancel()
	ev, err := probeTalkHotkey(ctx, combo)
	switch {
	case err != nil && ev.Kind == "":
		return config.Diagnostic{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      fmt.Sprintf("talk hotkey %s could not be probed: %v", label, err),
			Remediation: "the conversation falls back to Ctrl+G; rebuild with cgo on macOS, or set talk_hotkey_enabled: false",
		}
	case ev.Kind == talkhotkey.KindRegistered:
		return config.Diagnostic{
			Name:     "talk-hotkey",
			Severity: config.SeverityOK,
			Detail:   "talk hotkey " + label + " " + registeredCopy(),
		}
	case ev.Conflict():
		return config.Diagnostic{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + " " + conflictCopy(),
			Remediation: "quit the other owner or set talk_hotkey to a different combo, e.g. ctrl+alt+space",
		}
	default:
		return config.Diagnostic{
			Name:        "talk-hotkey",
			Severity:    config.SeverityWarn,
			Detail:      "talk hotkey " + label + " failed to register: " + ev.Detail,
			Remediation: "set talk_hotkey to a different combo, or talk_hotkey_enabled: false to use Ctrl+G only",
		}
	}
}

// registeredCopy says what a successful registration does and does not promise.
// A Mac registration proves nothing about other holders — macOS fires all of
// them — while a Windows one is exclusive by the time it succeeds.
func registeredCopy() string {
	if talkHotkeyOS == "windows" {
		return "registers (exclusive on Windows: a combo another program holds fails with error 1409)"
	}
	return "registers (macOS also lets other apps hold the same key; Raycast and Spotlight default to modifier+space)"
}

// conflictCopy names who could be holding the key. On macOS the known holders
// are worth listing; on Windows any program can hold one and none of those
// names apply.
func conflictCopy() string {
	if talkHotkeyOS == "windows" {
		return "is held by another program"
	}
	return "is owned by another app (the Obey Voice Mac app, Raycast, or a running obey-voice conversation)"
}

// talkControlDiag is the endpoint row. Nothing is wrong here, so it is an OK
// row with no remediation: it names the endpoint and the commands to bind, and
// that is the whole setup. Only the opening clause turns on whether this OS
// also has a key of its own.
func talkControlDiag(native bool) config.Diagnostic {
	endpoint := "recorded once a conversation starts"
	if _, address, err := talkhotkey.ControlEndpoint(); err == nil {
		endpoint = address
	}
	lead := "talk control: no native key on this OS; bind a shortcut to "
	if native {
		lead = "talk control: any shortcut tool can drive the key too — bind it to "
	}
	return config.Diagnostic{
		Name:     "talk-control",
		Severity: config.SeverityOK,
		Detail: lead + "`obey-voice talk down` / `up` " +
			"(or `press` with talk_hotkey_mode: toggle) — endpoint " + endpoint,
	}
}
