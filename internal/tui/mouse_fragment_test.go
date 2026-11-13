package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestSplitMouseReportsNeverReachTheComposerOrStopSpeech(t *testing.T) {
	for name, keys := range map[string][]tea.KeyMsg{
		"alt bracket then the rest":     {{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}, runes("<65;60;15M")},
		"one run":                       {runes("[<65;60;15M")},
		"cut inside a number":           {{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}, runes("<65;6"), runes("0;15M")},
		"one character at a time":       {runes("[<"), runes("6"), runes("5"), runes(";"), runes("6"), runes("0"), runes(";"), runes("1"), runes("5"), runes("M")},
		"two reports in one run":        {runes("[<65;60;15M[<64;60;15M")},
		"a click release, lower case m": {runes("[<0;10;4m")},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &sourceRecordingRunner{}
			m := sizedConversation(t, 100, 24)
			m.deps.runner = runner
			m.turnState = turnVoiceResponding
			for _, key := range keys {
				m, _ = m.Update(key)
			}
			stops := len(runner.sources)
			if got := m.input.Value(); got != "" {
				t.Fatalf("composer = %q, want empty", got)
			}
			if stops != 0 {
				t.Fatalf("a mouse report stopped speech %d time(s)", stops)
			}
			if m.mouseFragment != "" {
				t.Fatalf("fragment %q left pending", m.mouseFragment)
			}
		})
	}
}

func TestOrdinaryTypingIsNotMistakenForAMouseReport(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	for _, key := range []tea.KeyMsg{runes("["), runes("<"), runes("6"), runes("5"), runes(";"), runes(" "), runes("o"), runes("k")} {
		m, _ = m.Update(key)
	}
	if got := m.input.Value(); got != "[<65; ok" {
		t.Fatalf("composer = %q, want the typed text", got)
	}
}

func TestTypingStillStopsSpeech(t *testing.T) {
	runner := &sourceRecordingRunner{}
	m := sizedConversation(t, 100, 24)
	m.deps.runner = runner
	m.turnState = turnVoiceResponding
	m, _ = m.Update(runes("w"))
	if len(runner.sources) != 1 || m.input.Value() != "w" {
		t.Fatalf("typing: stops=%d composer=%q", len(runner.sources), m.input.Value())
	}
}
