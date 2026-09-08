package tui

import (
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func talkSettings(t *testing.T, cfg *config.Config, saved map[string]any) settingsModel {
	t.Helper()
	m := newSettings(cfg, nil)
	m.section = sectionTools
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.buildToolItems()
	return m
}

func typeTalkCombo(t *testing.T, m settingsModel, text string) settingsModel {
	t.Helper()
	for _, r := range text {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// The conversation's conflict message says "rebind in Settings → Tools", so
// the rows have to be there, spelled for this OS and marked as the default.
func TestToolsTabListsTheTalkKey(t *testing.T) {
	m := talkSettings(t, &config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}, map[string]any{})
	def, _ := talkhotkey.ParseCombo(talkhotkey.DefaultComboFor(runtime.GOOS))
	want := []string{"Talk key — ON ✓", "Talk key mode — hold", "Talk key combo — " + def.Display() + " (default)"}
	for i, row := range []int{toolRowTalkKey, toolRowTalkMode, toolRowTalkCombo} {
		if row >= len(m.toolItems) || m.toolItems[row] != want[i] {
			t.Fatalf("tools rows = %q, want row %d = %q", m.toolItems, row, want[i])
		}
	}
	if runtime.GOOS != "darwin" && strings.ContainsAny(strings.Join(m.toolItems, " "), "⌃⌥⇧⌘") {
		t.Fatalf("Apple glyphs on %s: %q", runtime.GOOS, m.toolItems)
	}
}

func TestTalkKeyToggleSaves(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{TalkHotkeyEnabled: true}, saved)
	m.cursor = toolRowTalkKey
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if saved["talk_hotkey_enabled"] != false || m.cfg.TalkHotkeyEnabled {
		t.Fatalf("saved = %v, cfg = %v; want the key turned off", saved, m.cfg.TalkHotkeyEnabled)
	}
	if !strings.Contains(m.toolItems[toolRowTalkKey], "OFF") || !strings.Contains(m.message, "next conversation") {
		t.Fatalf("row %q message %q", m.toolItems[toolRowTalkKey], m.message)
	}
}

func TestTalkKeyModeCyclesThroughEveryMode(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}, saved)
	m.cursor = toolRowTalkMode
	for _, want := range []string{"toggle", "automatic", "hold"} {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if saved["talk_hotkey_mode"] != want || m.cfg.TalkHotkeyMode != want {
			t.Fatalf("after enter: saved %v cfg %q, want %q", saved["talk_hotkey_mode"], m.cfg.TalkHotkeyMode, want)
		}
		if !strings.Contains(m.toolItems[toolRowTalkMode], want) {
			t.Fatalf("row = %q, want %s", m.toolItems[toolRowTalkMode], want)
		}
	}
}

func TestTalkKeyComboEditorSavesTheCanonicalSpelling(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{TalkHotkeyEnabled: true}, saved)
	m.cursor = toolRowTalkCombo
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.edit.kind != editTalkHotkey {
		t.Fatalf("enter on the combo row opened %v, want the combo editor", m.edit.kind)
	}
	m = typeTalkCombo(t, m, "Ctrl + Alt + T")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.edit.kind != editNone {
		t.Fatalf("editor still open with message %q", m.message)
	}
	if saved["talk_hotkey"] != "ctrl+alt+t" || m.cfg.TalkHotkey != "ctrl+alt+t" {
		t.Fatalf("saved %v cfg %q, want ctrl+alt+t", saved["talk_hotkey"], m.cfg.TalkHotkey)
	}
	combo, _ := talkhotkey.ParseCombo("ctrl+alt+t")
	if want := "Talk key combo — " + combo.Display(); m.toolItems[toolRowTalkCombo] != want {
		t.Fatalf("row = %q, want %q", m.toolItems[toolRowTalkCombo], want)
	}

	// Clearing the field returns to the platform default.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.edit.input.SetValue("")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if saved["talk_hotkey"] != "" || !strings.Contains(m.toolItems[toolRowTalkCombo], "(default)") {
		t.Fatalf("saved %v row %q, want the default", saved["talk_hotkey"], m.toolItems[toolRowTalkCombo])
	}
}

// A combo the conversation would refuse is refused here, with the editor
// left open so it can be fixed in place.
func TestTalkKeyComboEditorRefusesWhatTheConversationWould(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{TalkHotkeyEnabled: true}, saved)
	m.cursor = toolRowTalkCombo
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = typeTalkCombo(t, m, "shift+a")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.edit.kind != editTalkHotkey {
		t.Fatal("editor closed on an unusable combo")
	}
	if _, wrote := saved["talk_hotkey"]; wrote {
		t.Fatalf("an unusable combo was saved: %v", saved)
	}
	if !strings.Contains(m.message, "needs at least one of ctrl, alt or cmd") {
		t.Fatalf("message = %q, want the parser's reason", m.message)
	}
}
