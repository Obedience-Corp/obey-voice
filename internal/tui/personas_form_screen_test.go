package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func formScreenModel(t *testing.T) personasModel {
	t.Helper()
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha", TTSProvider: "kokoro"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "samantha", DisplayName: "Samantha", TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}},
		}, nil
	}
	m.starterPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.loadPromptForProfile = func(*persona.Profile) (string, error) {
		return "You are {agent_name}.\nBe brief.\nBe kind.", nil
	}
	m.directory.discover = func(context.Context) campdirs.Choices {
		t.Error("camp discovery ran without the test asking for it")
		return campdirs.Choices{}
	}
	m.reload()
	m.width, m.height = 88, 30
	return m
}

// The whole point of the one-screen form: model & voice is on screen the moment
// the form opens, with no editor to traverse first.
func TestPersonasFormShowsModelAndVoiceOnOpen(t *testing.T) {
	m := formScreenModel(t)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	view := stripANSI(m.View())
	for _, want := range []string{"Name", "Model & voice", "Brain", "Voice", "System prompt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("create form missing %q:\n%s", want, view)
		}
	}
	if m.promptOpen {
		t.Fatal("create opened straight into the prompt editor")
	}
}

// Model & voice must be reachable with arrows alone — the complaint that
// started this: name, then esc/:w to reach the settings people change most.
func TestPersonasFormReachesVoiceWithArrowsOnly(t *testing.T) {
	m := formScreenModel(t)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	for _, r := range "Buddy" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.nameInput.Value() != "Buddy" {
		t.Fatalf("name = %q", m.nameInput.Value())
	}
	// Down past brain provider/model and the two worker rows lands on TTS,
	// → picks a provider, ↓ → picks a voice.
	for i := 0; i < 5; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.formRow != formRowTTSProvider {
		t.Fatalf("formRow = %d, want the TTS row", m.formRow)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.ttsProviderIdx == 0 {
		t.Fatal("→ on the TTS row changed nothing")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.formRow != formRowVoice {
		t.Fatalf("formRow = %d, want the Voice row", m.formRow)
	}
	if m.promptOpen {
		t.Fatal("picking a voice went through the prompt editor")
	}

	var gotOpts persona.CreateOpts
	m.createPersona = func(_ *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
		gotOpts = opts
		return &persona.Profile{ID: "buddy", DisplayName: opts.DisplayName}, nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" {
		t.Fatalf("ctrl+j did not save from the form screen (message %q)", m.message)
	}
	if gotOpts.DisplayName != "Buddy" || gotOpts.TTS.Provider == "" {
		t.Fatalf("saved opts = %+v, want the name and the picked TTS provider", gotOpts)
	}
	if strings.TrimSpace(gotOpts.SystemPrompt) == "" {
		t.Fatal("a persona saved without opening the editor still needs a prompt body")
	}
}

// m on the list is the shortcut for the most-changed fields.
func TestPersonasMKeyOpensEditOnModelRow(t *testing.T) {
	m := formScreenModel(t)
	m.cursor = 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.formMode != "edit" {
		t.Fatalf("formMode = %q, want edit", m.formMode)
	}
	if m.formRow != formRowBrainProvider {
		t.Fatalf("formRow = %d, want the Brain row", m.formRow)
	}
	if m.editID != "samantha" {
		t.Fatalf("editID = %q", m.editID)
	}
	// e still opens on the name.
	m.cancelForm()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if m.formRow != formRowName {
		t.Fatalf("e opened on row %d, want the name row", m.formRow)
	}
}

// The collapsed prompt row has to say what it holds, or the form hides the
// field that matters most to the brain.
func TestPersonasPromptRowSummarizesTheDocument(t *testing.T) {
	m := formScreenModel(t)
	m.cursor = 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	view := stripANSI(m.View())
	if !strings.Contains(view, "You are {agent_name}.") {
		t.Fatalf("prompt row does not show the opening line:\n%s", view)
	}
	if !strings.Contains(view, "3 lines") || !strings.Contains(view, "enter to edit") {
		t.Fatalf("prompt row does not report its size:\n%s", view)
	}

	// enter opens the editor, and the body is the same document.
	m.setFormRow(formRowPrompt)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.promptOpen {
		t.Fatal("enter on the prompt row did not open the editor")
	}
	if !strings.Contains(stripANSI(m.View()), "Be kind.") {
		t.Fatalf("editor does not show the prompt body:\n%s", stripANSI(m.View()))
	}
}

// An empty prompt says so rather than rendering a blank row.
func TestPersonasPromptRowReportsEmptyDocument(t *testing.T) {
	m := formScreenModel(t)
	m.cursor = 0
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "", nil }
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !strings.Contains(stripANSI(m.View()), "(empty)") {
		t.Fatalf("empty prompt row not labelled:\n%s", stripANSI(m.View()))
	}
}
