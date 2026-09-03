package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func withTalkHotkeyProbe(t *testing.T, ev talkhotkey.Event, err error) {
	t.Helper()
	prev := probeTalkHotkey
	probeTalkHotkey = func(context.Context, talkhotkey.Combo) (talkhotkey.Event, error) { return ev, err }
	t.Cleanup(func() { probeTalkHotkey = prev })
}

func TestTalkHotkeyDiagsIsSilentWhenOffOrUnsupported(t *testing.T) {
	withTalkHotkeyProbe(t, talkhotkey.Event{}, errors.New("must not be called"))
	off := &config.Config{TalkHotkeyEnabled: false, TalkHotkey: "alt+space"}
	if got := talkHotkeyDiags(t.Context(), off); got != nil {
		t.Fatalf("disabled key produced %v", got)
	}
	// A combo that does not parse is the config layer's row, not this one.
	bad := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: "space"}
	if got := talkHotkeyDiags(t.Context(), bad); got != nil {
		t.Fatalf("unparsable combo produced %v", got)
	}
}

func TestTalkHotkeyDiagsLeavesTheKeyToTheMacApp(t *testing.T) {
	if !talkhotkey.Supported() {
		t.Skip("no helper on this platform")
	}
	withTalkHotkeyProbe(t, talkhotkey.Event{}, errors.New("must not probe while the app owns the key"))
	prev := talkhotkey.ProcessRunning
	talkhotkey.ProcessRunning = func(name string) bool { return name == "Obey Voice" }
	t.Cleanup(func() { talkhotkey.ProcessRunning = prev })

	cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: "alt+space", TalkHotkeyMode: "hold"}
	got := talkHotkeyDiags(t.Context(), cfg)
	if len(got) != 1 || got[0].Severity != config.SeverityWarn || !strings.Contains(got[0].Detail, "Mac app is running") {
		t.Fatalf("diags = %+v, want a warn naming the Mac app", got)
	}
}

func TestTalkHotkeyDiagsRows(t *testing.T) {
	if !talkhotkey.Supported() {
		t.Skip("no helper on this platform")
	}
	cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: "alt+space", TalkHotkeyMode: "hold"}
	for name, tc := range map[string]struct {
		ev       talkhotkey.Event
		err      error
		severity config.Severity
		detail   string
	}{
		"registered": {talkhotkey.Event{Kind: talkhotkey.KindRegistered, Combo: "alt+space"}, nil, config.SeverityOK, "⌥Space (hold) registers"},
		"conflict":   {talkhotkey.Event{Kind: talkhotkey.KindError, Code: talkhotkey.ConflictCode, Detail: "taken"}, errors.New("exit status 1"), config.SeverityWarn, "owned by another app"},
		"other":      {talkhotkey.Event{Kind: talkhotkey.KindError, Code: -50, Detail: "paramErr"}, errors.New("exit status 1"), config.SeverityWarn, "failed to register: paramErr"},
		"no helper":  {talkhotkey.Event{}, errors.New("exec: not found"), config.SeverityWarn, "could not be probed"},
	} {
		t.Run(name, func(t *testing.T) {
			prev := talkhotkey.ProcessRunning
			talkhotkey.ProcessRunning = func(string) bool { return false }
			t.Cleanup(func() { talkhotkey.ProcessRunning = prev })
			withTalkHotkeyProbe(t, tc.ev, tc.err)
			got := talkHotkeyDiags(t.Context(), cfg)
			if len(got) != 1 || got[0].Name != "talk-hotkey" {
				t.Fatalf("diags = %+v, want one talk-hotkey row", got)
			}
			if got[0].Severity != tc.severity || !strings.Contains(got[0].Detail, tc.detail) {
				t.Fatalf("row = %+v, want %s containing %q", got[0], tc.severity, tc.detail)
			}
		})
	}
}

func withNativeTalkHotkey(t *testing.T, native bool) {
	t.Helper()
	prev := nativeTalkHotkey
	nativeTalkHotkey = func() bool { return native }
	t.Cleanup(func() { nativeTalkHotkey = prev })
}

// Where no process may claim a key, doctor reports the working path rather
// than a missing one: an OK row naming the endpoint and the commands to bind.
func TestTalkHotkeyDiagsReportsTheControlEndpoint(t *testing.T) {
	withNativeTalkHotkey(t, false)
	withTalkHotkeyProbe(t, talkhotkey.Event{}, errors.New("must not probe without a native key"))

	cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}
	got := talkHotkeyDiags(t.Context(), cfg)
	if len(got) != 1 || got[0].Name != "talk-hotkey" {
		t.Fatalf("diags = %+v, want one talk-hotkey row", got)
	}
	if got[0].Severity != config.SeverityOK || got[0].Remediation != "" {
		t.Fatalf("row = %+v, want an OK row with nothing to fix", got[0])
	}
	for _, want := range []string{"obey-voice talk down", "talk_hotkey_mode: toggle", "endpoint "} {
		if !strings.Contains(got[0].Detail, want) {
			t.Fatalf("detail %q lacks %q", got[0].Detail, want)
		}
	}
}

// The native rows are unchanged where a key can be claimed.
func TestTalkHotkeyDiagsKeepsTheNativeRowWithAHelper(t *testing.T) {
	if !talkhotkey.Supported() {
		t.Skip("no helper on this platform")
	}
	withNativeTalkHotkey(t, true)
	prev := talkhotkey.ProcessRunning
	talkhotkey.ProcessRunning = func(string) bool { return false }
	t.Cleanup(func() { talkhotkey.ProcessRunning = prev })
	withTalkHotkeyProbe(t, talkhotkey.Event{Kind: talkhotkey.KindRegistered, Combo: "alt+space"}, nil)

	cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: "alt+space", TalkHotkeyMode: "hold"}
	got := talkHotkeyDiags(t.Context(), cfg)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "⌥Space (hold) registers") {
		t.Fatalf("diags = %+v, want the native registration row", got)
	}
}
