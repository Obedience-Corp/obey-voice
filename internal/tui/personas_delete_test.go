package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// personasWithDelete builds a two-persona screen wired to a recording delete
// seam; the returned pointer holds the id the seam was called with.
func personasWithDelete(t *testing.T, items []*persona.Profile) (personasModel, *[]string) {
	t.Helper()
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) { return items, nil }
	deleted := &[]string{}
	m.deletePersona = func(_ *config.Config, id string) (*persona.DeleteResult, error) {
		*deleted = append(*deleted, id)
		return &persona.DeleteResult{ID: id, ActivePersona: "samantha"}, nil
	}
	m.reload()
	m.width, m.height = 80, 24
	return m, deleted
}

func pressKey(m personasModel, r rune) personasModel {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return m
}

func TestPersonasDeleteAsksBeforeRemoving(t *testing.T) {
	items := []*persona.Profile{
		{ID: "samantha", DisplayName: "Samantha"},
		{ID: "festival", DisplayName: "Festival"},
	}
	m, deleted := personasWithDelete(t, items)
	m.cursor = 1

	m = pressKey(m, 'd')
	if m.confirmDeleteID != "festival" {
		t.Fatalf("confirmDeleteID = %q, want festival", m.confirmDeleteID)
	}
	if len(*deleted) != 0 {
		t.Fatalf("delete ran before confirmation: %v", *deleted)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Confirm delete", "Delete Festival?", "cannot be undone", "y delete permanently"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirm view missing %q:\n%s", want, view)
		}
	}

	m = pressKey(m, 'y')
	if len(*deleted) != 1 || (*deleted)[0] != "festival" {
		t.Fatalf("deleted = %v, want [festival]", *deleted)
	}
	if m.confirmDeleteID != "" {
		t.Fatalf("confirmDeleteID = %q after delete, want empty", m.confirmDeleteID)
	}
	if !strings.Contains(m.message, "Deleted Festival") {
		t.Fatalf("message = %q", m.message)
	}
}

func TestPersonasDeleteCancelsOnAnyOtherKey(t *testing.T) {
	// The point of the prompt: a stray keystroke must never destroy a profile.
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{' '}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'d'}},
	} {
		t.Run(key.String(), func(t *testing.T) {
			items := []*persona.Profile{
				{ID: "samantha", DisplayName: "Samantha"},
				{ID: "festival", DisplayName: "Festival"},
			}
			m, deleted := personasWithDelete(t, items)
			m.cursor = 1
			m = pressKey(m, 'd')

			m, _ = m.Update(key)
			if len(*deleted) != 0 {
				t.Fatalf("%s deleted %v", key.String(), *deleted)
			}
			if m.confirmDeleteID != "" {
				t.Fatalf("confirmation still armed after %s", key.String())
			}
			if !strings.Contains(m.message, "Kept Festival") {
				t.Fatalf("message = %q", m.message)
			}
			if !strings.Contains(stripANSI(m.View()), "Festival") {
				t.Fatal("list not restored after cancel")
			}
		})
	}
}

func TestPersonasDeleteRefusesBuiltinAndLastPersona(t *testing.T) {
	t.Run("builtin", func(t *testing.T) {
		items := []*persona.Profile{
			{ID: persona.DefaultID, DisplayName: "Obey", Builtin: true},
			{ID: "festival", DisplayName: "Festival"},
		}
		m, deleted := personasWithDelete(t, items)
		m.cursor = 0
		m = pressKey(m, 'd')
		if m.confirmDeleteID != "" {
			t.Fatal("built-in persona armed a delete confirmation")
		}
		if len(*deleted) != 0 {
			t.Fatalf("deleted = %v", *deleted)
		}
		if !strings.Contains(m.message, "built-in") {
			t.Fatalf("message = %q", m.message)
		}
	})

	t.Run("last", func(t *testing.T) {
		items := []*persona.Profile{{ID: "festival", DisplayName: "Festival"}}
		m, deleted := personasWithDelete(t, items)
		m.cursor = 0
		m = pressKey(m, 'd')
		if m.confirmDeleteID != "" {
			t.Fatal("only persona armed a delete confirmation")
		}
		if len(*deleted) != 0 {
			t.Fatalf("deleted = %v", *deleted)
		}
		if !strings.Contains(m.message, "only persona") {
			t.Fatalf("message = %q", m.message)
		}
	})
}

func TestPersonasDeleteOnCreateRowDoesNothing(t *testing.T) {
	items := []*persona.Profile{
		{ID: "samantha", DisplayName: "Samantha"},
		{ID: "festival", DisplayName: "Festival"},
	}
	m, deleted := personasWithDelete(t, items)
	m.cursor = len(items) // trailing "+ Create new persona…" row
	m = pressKey(m, 'd')
	if m.confirmDeleteID != "" || len(*deleted) != 0 {
		t.Fatalf("create row armed a delete: id=%q deleted=%v", m.confirmDeleteID, *deleted)
	}
}

func TestPersonasDeleteReportsReactivationAndKeptPrompt(t *testing.T) {
	items := []*persona.Profile{
		{ID: "samantha", DisplayName: "Samantha"},
		{ID: "festival", DisplayName: "Festival"},
	}
	cfg := &config.Config{ActivePersona: "festival", AgentName: "Festival"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) { return items, nil }
	m.deletePersona = func(_ *config.Config, id string) (*persona.DeleteResult, error) {
		items = items[:1]
		return &persona.DeleteResult{
			ID:            id,
			Kept:          []string{"/home/u/.obey/prompts/persona/shared.yaml"},
			ActivePersona: "samantha",
			Reactivated:   true,
		}, nil
	}
	m.reload()
	m.width, m.height = 80, 24
	m.cursor = 1

	m = pressKey(m, 'd')
	if !strings.Contains(stripANSI(m.View()), "active persona") {
		t.Fatalf("confirm view does not warn about deleting the active persona:\n%s", stripANSI(m.View()))
	}
	m = pressKey(m, 'y')

	if !strings.Contains(m.message, "kept shared prompt shared.yaml") {
		t.Fatalf("message = %q, want kept prompt", m.message)
	}
	if !strings.Contains(m.message, "active persona is now samantha") {
		t.Fatalf("message = %q, want reactivation", m.message)
	}
	if len(m.items) != 1 {
		t.Fatalf("list not reloaded: %d items", len(m.items))
	}
	if m.cursor >= m.listLen() {
		t.Fatalf("cursor %d out of range for %d rows", m.cursor, m.listLen())
	}
}

func TestPersonasDeleteSurfacesFailure(t *testing.T) {
	items := []*persona.Profile{
		{ID: "samantha", DisplayName: "Samantha"},
		{ID: "festival", DisplayName: "Festival"},
	}
	m, _ := personasWithDelete(t, items)
	m.deletePersona = func(*config.Config, string) (*persona.DeleteResult, error) {
		return nil, errors.New("permission denied")
	}
	m.cursor = 1
	m = pressKey(m, 'd')
	m = pressKey(m, 'y')
	if !strings.Contains(m.message, "Failed to delete Festival") || !strings.Contains(m.message, "permission denied") {
		t.Fatalf("message = %q", m.message)
	}
	if len(m.items) != 2 {
		t.Fatalf("list changed after a failed delete: %d items", len(m.items))
	}
}
