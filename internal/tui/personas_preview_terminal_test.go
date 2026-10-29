package tui

import (
	"bytes"
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
	"time"
)

func TestPersonaPreviewTerminalCreateAndEdit(t *testing.T) {
	for _, edit := range []bool{false, true} {
		m := formScreenModel(t)
		if edit {
			m.beginEditAt(formRowVoice)
		} else {
			m.beginCreate()
			m.setFormRow(formRowVoice)
		}
		m.selectedVoice = "af_bella"
		m.preview.newPreviewPlayer = func() audio.Engine { return newFakePreviewPlayer() }
		m.preview.ensure = func(context.Context, *config.Config, func(string, float64)) error { return nil }
		m.preview.newTTS = func(*config.Config) (tts.Provider, func(), error) { return fakePreviewTTS{}, nil, nil }
		h := &personaPreviewTerminal{model: m}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var output bytes.Buffer
		program := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler())
		_, err := program.Run()
		cancel()
		h.model.preview.close()
		if err != nil {
			t.Fatal(err)
		}
		rendered := stripANSI(output.String())
		for _, want := range []string{"[p preview]", "Previewed af_bella"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("terminal missing %q: %s", want, rendered)
			}
		}
		if h.model.selectedVoice != "af_bella" || h.model.formMode == "" {
			t.Fatal("preview saved or discarded the form")
		}
	}
}

func TestPersonaListPreviewTerminal(t *testing.T) {
	m := listPreviewModel(t)
	h := &personaPreviewTerminal{model: m}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	program := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler())
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	h.model.preview.close()
	for _, want := range []string{"p preview/stop", "Previewed am_santa"} {
		if !strings.Contains(stripANSI(output.String()), want) {
			t.Fatalf("terminal missing %q: %s", want, stripANSI(output.String()))
		}
	}
	if h.model.formMode != "" || h.model.cfg.ActivePersona != "samantha" {
		t.Fatal("list preview changed selection")
	}
}

type personaPreviewTerminal struct{ model personasModel }

func (h *personaPreviewTerminal) Init() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}} })
}
func (h *personaPreviewTerminal) View() string { return h.model.View() }
func (h *personaPreviewTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if _, ok := msg.(voicePreviewDoneMsg); ok {
		return h, tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tea.Quit() })
	}
	return h, cmd
}
