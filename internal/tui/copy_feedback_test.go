package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func lastNotice(m conversationModel) string {
	for i := len(m.items) - 1; i >= 0; i-- {
		if m.items[i].notice != "" {
			return m.items[i].notice
		}
	}
	return ""
}

func TestThinkToggleExplainsAnEmptyReveal(t *testing.T) {
	t.Run("ollama thinking off names the setting", func(t *testing.T) {
		m := sizedConversation(t, 100, 24)
		m.cfg = &config.Config{BrainProvider: "ollama", OllamaThink: "auto"}
		m.handleEvent(events.ResponseReady{Response: "Hello."})
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
		got := lastNotice(m)
		if !strings.Contains(got, "not asked to think") || !strings.Contains(got, "Settings → Brain") {
			t.Fatalf("notice = %q, want the reason and the setting", got)
		}
	})
	t.Run("ollama thinking on only says none arrived", func(t *testing.T) {
		m := sizedConversation(t, 100, 24)
		m.cfg = &config.Config{BrainProvider: "ollama", OllamaThink: "true"}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
		got := lastNotice(m)
		if !strings.Contains(got, "no reply in this chat") || strings.Contains(got, "Settings") {
			t.Fatalf("notice = %q", got)
		}
	})
	t.Run("silent when the thread has thinking", func(t *testing.T) {
		m := sizedConversation(t, 100, 24)
		m.cfg = &config.Config{BrainProvider: "ollama", OllamaThink: "auto"}
		m.handleEvent(events.ThinkingDelta{Text: "weighing it"})
		m.handleEvent(events.ResponseReady{Response: "Hello."})
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
		if got := lastNotice(m); got != "" {
			t.Fatalf("notice = %q, want none", got)
		}
	})
	t.Run("silent when hiding", func(t *testing.T) {
		m := sizedConversation(t, 100, 24)
		m.cfg = &config.Config{BrainProvider: "ollama", OllamaThink: "auto"}
		m.showThink = true
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
		if got := lastNotice(m); got != "" {
			t.Fatalf("notice = %q, want none", got)
		}
	})
}

func TestMouseDragExplainsSelectionOnce(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	drag := tea.MouseMsg{X: 4, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
	m, _ = m.Update(drag)
	if got := lastNotice(m); got != selectHint {
		t.Fatalf("notice = %q, want the select hint", got)
	}
	before := len(m.items)
	m, _ = m.Update(drag)
	if len(m.items) != before {
		t.Fatal("the select hint repeated on a second drag")
	}

	clicked := sizedConversation(t, 100, 24)
	clicked, _ = clicked.Update(tea.MouseMsg{X: 4, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	clicked, _ = clicked.Update(tea.MouseMsg{X: 4, Y: 4, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if got := lastNotice(clicked); got != "" {
		t.Fatalf("a click or hover showed %q", got)
	}
}

func TestCopyActivityCopiesTheLog(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	clip := &testClipboard{}
	m.deps.clipboard = clip
	m.appendActivity("model", "first token", 0)
	m.runCopyCommand([]string{"activity"})
	if !strings.Contains(clip.value, "model  first token") {
		t.Fatalf("clipboard = %q, want the activity lines", clip.value)
	}
	if strings.Contains(clip.value, "\x1b") {
		t.Fatalf("clipboard carries terminal styling: %q", clip.value)
	}
}
