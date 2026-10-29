package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestDefaultPersonaTerminalUsesSavedObeyStack(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	profile := &persona.Profile{Schema: persona.Schema, ID: persona.DefaultID, DisplayName: "Obey", Prompts: persona.PromptRefs{Persona: persona.DefaultID}, Brain: persona.Brain{Provider: "ollama", Model: "lfm3:1.7b", Worker: persona.Worker{Provider: "codex", Model: "worker-model"}}, TTS: persona.TTS{Provider: "openai-speech", Voice: "Teddy"}}
	if err := persona.Write(profile, false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", OllamaModel: "dolphin-mistral", TTSProvider: "openai-speech", OpenAISpeechVoice: "Zoey"}
	m := newLauncher(cfg, nil)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{profile, {ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m, _ = m.openPersonaPicker()
	h := &defaultPersonaTerminal{model: m, cfg: cfg}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
	if h.err != nil || h.binding == nil {
		t.Fatalf("default persona did not bind: %v", h.err)
	}
	got := h.binding.Config()
	if got.OllamaModel != "lfm3:1.7b" || got.OpenAISpeechVoice != "Teddy" || got.VoiceWorkerProvider != "codex" || got.VoiceWorkerModel != "worker-model" {
		t.Fatalf("default chat used a mixed stack: %+v", h.binding)
	}
	rendered := stripANSI(output.String())
	for _, want := range []string{"Obey (default)", "lfm3:1.7b", "Teddy"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("default picker hides %q: %s", want, rendered)
		}
	}
	if len(m.submenu) != 3 {
		t.Fatal("default Obey was duplicated in the persona roster")
	}
	if cfg.OllamaModel != "dolphin-mistral" || cfg.OpenAISpeechVoice != "Zoey" {
		t.Fatal("starting Obey rewrote global Settings")
	}
}

type defaultPersonaTerminal struct {
	model   launcherModel
	cfg     *config.Config
	binding *persona.SessionBinding
	err     error
}

func (*defaultPersonaTerminal) Init() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} })
}
func (h *defaultPersonaTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if start, ok := msg.(startPipelineMsg); ok {
		if start.personaID == StartDefaultsID {
			h.binding, h.err = persona.ResolveDefaultsBinding(h.cfg)
		} else {
			h.binding, h.err = persona.ResolveBinding(h.cfg, start.personaID)
		}
		return h, tea.Quit
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, cmd
}
func (h *defaultPersonaTerminal) View() string { return h.model.View() }
