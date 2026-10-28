package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	tea "github.com/charmbracelet/bubbletea"
)

func mentionConversation(t *testing.T) conversationModel {
	t.Helper()
	m := sizedConversation(t, 100, 30)
	m.mentions.load = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "joker", DisplayName: "The Joker"}, {ID: "jarvis", DisplayName: "Jarvis"},
			{ID: "uncle-fu", DisplayName: "Uncle Fu", Aliases: []string{"master"}},
			{ID: "hidden", DisplayName: "Hidden", Inactive: true},
		}, nil
	}
	return m
}

func setMentionDraft(m *conversationModel, text string) {
	previous := m.input.Value()
	m.input.SetValue(text)
	m.moveTextareaCursorToOffset(len([]rune(text)))
	m.syncEditorFromTextarea()
	m.syncComposer(previous)
}

func TestPersonaMentionCompletion(t *testing.T) {
	m := mentionConversation(t)
	setMentionDraft(&m, "@")
	view := stripANSI(m.View())
	if !strings.Contains(view, "@joker") || !strings.Contains(view, "@uncle-fu") || strings.Contains(view, "@hidden") {
		t.Fatalf("persona palette: %s", view)
	}
	setMentionDraft(&m, "@jkr")
	if len(m.mentions.matches) != 1 || m.mentions.matches[0].id != "joker" {
		t.Fatalf("fuzzy candidates=%+v", m.mentions.matches)
	}
	m.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "@joker " || m.mentions.active {
		t.Fatalf("completed input=%q", m.input.Value())
	}
	setMentionDraft(&m, "@mstr")
	m.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "@uncle-fu " {
		t.Fatal("alias did not complete to canonical ID")
	}
	setMentionDraft(&m, "email me at joker@example.com")
	if m.mentions.active {
		t.Fatal("email opened persona completion")
	}
	setMentionDraft(&m, "@nobody")
	if !strings.Contains(stripANSI(m.View()), "No matching personas") || m.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab}) {
		t.Fatal("missing persona silently completed")
	}
}

func TestPersonaMentionNavigationAndDraftPreservation(t *testing.T) {
	m := mentionConversation(t)
	setMentionDraft(&m, "@j")
	m.handleMentionKey(tea.KeyMsg{Type: tea.KeyDown})
	m.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "@joker " {
		t.Fatalf("selected=%q", m.input.Value())
	}
	setMentionDraft(&m, "@jkr please help")
	m.moveTextareaCursorToOffset(4)
	m.syncEditorFromTextarea()
	m.syncComposer(m.input.Value())
	m.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "@joker please help" {
		t.Fatalf("draft lost: %q", m.input.Value())
	}
}
