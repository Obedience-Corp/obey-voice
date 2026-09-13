package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestPersonasTerminalTogglePersistsInactiveAndRefusesSelection(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	p := &persona.Profile{Schema: persona.Schema, ID: "experiment", DisplayName: "Experiment", Prompts: persona.PromptRefs{Persona: "experiment"}}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ActivePersona: "obey"}
	for _, disable := range []bool{true, false} {
		model := newPersonas(cfg)
		model.width, model.height = 110, 24
		model.usePersona = func(*config.Config, string) error {
			t.Fatal("inactive persona was selected")
			return nil
		}
		keys := []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'x'}}}
		want := "Experiment is enabled"
		if disable {
			keys = append(keys, tea.KeyMsg{Type: tea.KeyEnter})
			want = "Experiment is inactive; press x to enable it"
		}
		h := &inactivePersonaTerminal{model: model, keys: keys}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var terminal bytes.Buffer
		_, err := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler()).Run()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		rendered := stripANSI(terminal.String())
		if !strings.Contains(rendered, want) || (disable && !strings.Contains(rendered, "[inactive]")) {
			t.Fatalf("missing state/feedback in terminal:\n%s", rendered)
		}
		stored, err := persona.Load(p.ID)
		if err != nil || stored.Inactive != disable || stored.Prompts != p.Prompts {
			t.Fatalf("persisted persona = %+v, %v", stored, err)
		}
	}
}

type inactivePersonaTerminal struct {
	model personasModel
	keys  []tea.KeyMsg
}

func (h *inactivePersonaTerminal) next() tea.Cmd {
	if len(h.keys) == 0 {
		return tea.Quit
	}
	key := h.keys[0]
	h.keys = h.keys[1:]
	return func() tea.Msg { return key }
}

func (h *inactivePersonaTerminal) Init() tea.Cmd { return h.next() }
func (h *inactivePersonaTerminal) View() string  { return h.model.View() }
func (h *inactivePersonaTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if _, ok := msg.(tea.KeyMsg); ok {
		return h, tea.Sequence(cmd, h.next())
	}
	return h, cmd
}

func TestPersonasCannotDisableConversationResponder(t *testing.T) {
	m := personasModel{
		items:           []*persona.Profile{{ID: "live", DisplayName: "Live"}},
		selectedPersona: func() string { return "live" },
		setInactive: func(*config.Config, string, bool) (*persona.Profile, error) {
			t.Fatal("must refuse before writing")
			return nil, nil
		},
	}
	m.toggleInactive()
	if !strings.Contains(m.message, "Select another persona in this conversation") {
		t.Fatalf("refusal = %q", m.message)
	}
}

func TestLauncherOmitsInactivePersonaFromNewConversation(t *testing.T) {
	m := launcherModel{listPersonas: func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "enabled"}, {ID: "inactive", Inactive: true}}, nil
	}}
	m, _ = m.openPersonaPicker()
	found := false
	for _, item := range m.submenu {
		if item.personaID == "inactive" {
			t.Fatal("inactive persona offered for new conversation")
		}
		found = found || item.personaID == "enabled"
	}
	if !found {
		t.Fatal("enabled persona missing from picker")
	}
}
