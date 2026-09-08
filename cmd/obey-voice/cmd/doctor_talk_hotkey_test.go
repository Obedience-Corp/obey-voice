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

	cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: talkhotkey.DefaultCombo, TalkHotkeyMode: "hold"}
	got := talkHotkeyDiags(t.Context(), cfg)
	if len(got) != 2 || got[0].Severity != config.SeverityWarn || !strings.Contains(got[0].Detail, "Mac app is running") {
		t.Fatalf("diags = %+v, want a warn naming the Mac app, then the endpoint row", got)
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
			if len(got) != 2 || got[0].Name != "talk-hotkey" || got[1].Name != "talk-control" {
				t.Fatalf("diags = %+v, want a talk-hotkey row then a talk-control row", got)
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
	if len(got) != 1 || got[0].Name != "talk-control" {
		t.Fatalf("diags = %+v, want one talk-control row", got)
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
	if len(got) != 2 || !strings.Contains(got[0].Detail, "⌥Space (hold) registers") {
		t.Fatalf("diags = %+v, want the native registration row", got)
	}
	// The docs say doctor prints the endpoint. It has to print it here too,
	// where a native key exists: the commands work on every platform.
	if got[1].Name != "talk-control" || !strings.Contains(got[1].Detail, "endpoint ") {
		t.Fatalf("second row = %+v, want the endpoint alongside the native key", got[1])
	}
}

func withTalkHotkeyOS(t *testing.T, goos string) {
	t.Helper()
	previous := talkHotkeyOS
	talkHotkeyOS = goos
	t.Cleanup(func() { talkHotkeyOS = previous })
}

// The two platforms hand out a hot key differently, so the rows say different
// things: macOS shares one between every registrant, Windows gives it to one
// process and fails the rest with 1409.
func TestTalkHotkeyDiagsCopyFollowsThePlatform(t *testing.T) {
	for name, tc := range map[string]struct {
		goos         string
		conflictCode int
		registered   string
		conflict     string
	}{
		"windows": {
			goos:         "windows",
			conflictCode: talkhotkey.ConflictCodeWindows,
			registered:   "registers (exclusive on Windows: a combo another program holds fails with error 1409)",
			conflict:     "is held by another program",
		},
		"darwin": {
			goos:         "darwin",
			conflictCode: talkhotkey.ConflictCode,
			registered:   "registers (macOS also lets other apps hold the same key; Raycast and Spotlight default to modifier+space)",
			conflict:     "is owned by another app (the Obey Voice Mac app, Raycast, or a running obey-voice conversation)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			withNativeTalkHotkey(t, true)
			withTalkHotkeyOS(t, tc.goos)
			previous := talkhotkey.ProcessRunning
			talkhotkey.ProcessRunning = func(string) bool { return false }
			t.Cleanup(func() { talkhotkey.ProcessRunning = previous })
			cfg := &config.Config{TalkHotkeyEnabled: true, TalkHotkey: "ctrl+alt+space", TalkHotkeyMode: "hold"}

			withTalkHotkeyProbe(t, talkhotkey.Event{Kind: talkhotkey.KindRegistered}, nil)
			ok := talkHotkeyDiags(t.Context(), cfg)
			if len(ok) != 2 || !strings.HasSuffix(ok[0].Detail, tc.registered) {
				t.Fatalf("registered row = %+v, want it to end %q", ok[0], tc.registered)
			}

			withTalkHotkeyProbe(t, talkhotkey.Event{Kind: talkhotkey.KindError, Code: tc.conflictCode}, errors.New("exit status 1"))
			taken := talkHotkeyDiags(t.Context(), cfg)
			if len(taken) != 2 || !strings.HasSuffix(taken[0].Detail, tc.conflict) {
				t.Fatalf("conflict row = %+v, want it to end %q", taken[0], tc.conflict)
			}
			if taken[1].Name != "talk-control" {
				t.Fatalf("second row = %+v, want the endpoint row", taken[1])
			}
		})
	}
}
