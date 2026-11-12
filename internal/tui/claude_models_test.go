package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/fakeclaude"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// claudeOnlyDiscovery is the form's real scan narrowed to Claude, so a test
// starts only the stand-in CLI and no other provider's.
func claudeOnlyDiscovery(ctx context.Context, cfg *config.Config) []discovery.ProviderInfo {
	return []discovery.ProviderInfo{discovery.DiscoverProvider(ctx, cfg, "claude")}
}

// claudePersona is a persona on Claude in the test's own persona store.
func claudePersona(t *testing.T) *persona.Profile {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	p := &persona.Profile{
		Schema: persona.Schema, ID: "writer", DisplayName: "Writer",
		Prompts: persona.PromptRefs{Persona: "obey"},
		Brain:   persona.Brain{Provider: "claude"},
	}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}
	return p
}

// openClaudeModelRow opens the edit form, lands its model scan, and puts the
// cursor on the Model row.
func openClaudeModelRow(t *testing.T, m personasModel) personasModel {
	t.Helper()
	m.brainModels.discover = claudeOnlyDiscovery
	m.cursor = 0
	scan := personaScanCmd(t, m.beginEditAt(formRowName))
	m, _ = m.Update(scan())
	if !m.brainModels.loaded {
		t.Fatalf("model scan did not land: %q", m.brainModels.err)
	}
	m.setFormRow(formRowBrainProvider)
	m.setFormRow(formRowBrainModel)
	return m
}

func TestPersonaFormListsTheClaudeCLIModels(t *testing.T) {
	p := claudePersona(t)
	cli := fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, p)
	m.saveName = func(id, name string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.savePrompt = func(id, prompt string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	// saveStack stays persona.UpdateStack, so the pick is written to the store.
	m = openClaudeModelRow(t, m)

	want := []string{stackDefaultLabel, "sonnet", "opus", "haiku", "sonnet[1m]", stackCustomLabel}
	if got := m.stackModelList(); !slices.Equal(got, want) {
		t.Fatalf("Model row = %q, want %q", got, want)
	}
	if note := m.stackModelNote(); note != "" {
		t.Fatalf("listed models still carry the note %q", note)
	}
	if line := modelRowLine(t, m); strings.Contains(line, "no model list") {
		t.Fatalf("Model row claims no list:\n%s", line)
	}

	for range 4 {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
		_ = m.View()
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.formBrain().Model; got != "sonnet[1m]" {
		t.Fatalf("picked %q, want sonnet[1m]", got)
	}
	if runs := cli.Runs(t); len(runs) != 1 {
		t.Fatalf("the open form asked the CLI %d times, want once", len(runs))
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" {
		t.Fatalf("save did not close the form: %s", m.message)
	}
	saved, err := persona.Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Brain.Provider != "claude" || saved.Brain.Model != "sonnet[1m]" {
		t.Fatalf("saved brain = %+v", saved.Brain)
	}

	// Each form open asks again: what "default" resolves to can change.
	openClaudeModelRow(t, m)
	if runs := cli.Runs(t); len(runs) != 2 {
		t.Fatalf("reopening the form asked the CLI %d times in all, want 2", len(runs))
	}
}

func TestPersonaFormKeepsTheClaudeFallbackNote(t *testing.T) {
	p := claudePersona(t)
	cli := fakeclaude.OnPath(t, fakeclaude.TooOld, "")
	m := openClaudeModelRow(t, newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, p))

	if got, want := m.stackModelList(), []string{stackDefaultLabel, stackCustomLabel}; !slices.Equal(got, want) {
		t.Fatalf("Model row = %q, want %q", got, want)
	}
	if note := m.stackModelNote(); !strings.Contains(note, "claude reports no model list") {
		t.Fatalf("note = %q, want the fallback wording", note)
	}
	if line := modelRowLine(t, m); !strings.Contains(line, "no model list") {
		t.Fatalf("Model row hides the fallback note:\n%s", line)
	}
	if runs := cli.Runs(t); len(runs) != 1 {
		t.Fatalf("the CLI ran %d times, want 1", len(runs))
	}
}

func TestClaudeSettingsListsTheCLIModels(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	cfg := &config.Config{BrainProvider: "claude", ClaudeModel: "claude-opus-4-1"}
	m := settingsModel{cfg: cfg, providers: []discovery.ProviderInfo{discovery.DiscoverProvider(t.Context(), cfg, "claude")}}
	m.buildModelItems()

	row := m.modelRows()[0]
	want := []string{"default", "sonnet", "opus", "haiku", "sonnet[1m]", "claude-opus-4-1"}
	if !slices.Equal(row.options, want) || row.current != len(want)-1 {
		t.Fatalf("Model row = %q at %d, want %q with the saved model selected", row.options, row.current, want)
	}
	row.set(&m, 4)
	saved, err := config.LoadRaw()
	if err != nil || cfg.ClaudeModel != "sonnet[1m]" || saved.ClaudeModel != "sonnet[1m]" {
		t.Fatalf("model = %q, saved = %q, err = %v", cfg.ClaudeModel, saved.ClaudeModel, err)
	}
}

// The app asks the CLI once at startup; opening Settings reuses that answer.
func TestClaudeModelsAreAskedAtStartupOnly(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cli := fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	useRealDiscovery(t)
	app := NewApp(&config.Config{BrainProvider: "claude", OllamaHost: "http://127.0.0.1:1"})
	for range 2 {
		opened, _ := app.onSwitchScreen(switchScreenMsg(screenSettings))
		app = opened.(App)
	}
	if got, want := app.settings.modelRows()[0].options, []string{"default", "sonnet", "opus", "haiku", "sonnet[1m]"}; !slices.Equal(got, want) {
		t.Fatalf("Settings Model row = %q, want %q", got, want)
	}
	if runs := cli.Runs(t); len(runs) != 1 {
		t.Fatalf("the CLI ran %d times, want once at startup", len(runs))
	}
}
