package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

// Exercise the owning manager's list, key handling and actual terminal renderer.
func TestPersonasTerminalRetainsHealthySwitchAndRepairWarning(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := persona.Write(&persona.Profile{Schema: persona.Schema, ID: "samantha", DisplayName: "Samantha", Prompts: persona.PromptRefs{Persona: "samantha"}}, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(persona.Dir(), "broken", "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config.Set("brain_provider", "ollama")
	config.Set("ollama_model", "terminal-model:8b")
	model := newPersonas(&config.Config{ActivePersona: "samantha"})
	model.width, model.height = 100, 24
	var selected string
	model.usePersona = func(_ *config.Config, id string) error { selected = id; return nil }
	harness := &personaWarningTerminal{model: model}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var terminal bytes.Buffer
	program := tea.NewProgram(harness, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	rendered := stripANSI(terminal.String())
	for _, want := range []string{"terminal-model:8b", "LLM provider", "LLM model", "Samantha", "Unavailable: broken", "repair persona.yaml", "Active persona:"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q from terminal:\n%s", want, rendered)
		}
	}
	if selected != "samantha" {
		t.Fatalf("healthy switch = %q", selected)
	}
	t.Logf("rendered manager:\n%s", rendered)
}

type personaWarningTerminal struct{ model personasModel }

func (h *personaWarningTerminal) Init() tea.Cmd {
	return func() tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} }
}
func (h *personaWarningTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEnter {
		return h, tea.Quit
	}
	return h, cmd
}
func (h *personaWarningTerminal) View() string { return h.model.View() }
