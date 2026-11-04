package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Editing a persona must hand back the worker it was saved with. A form that
// silently drops brain.worker would quietly retire the choice on every save.
func TestPersonaFormRoundTripsTheWorker(t *testing.T) {
	p := &persona.Profile{
		ID:          "jarvis",
		DisplayName: "Jarvis",
		Brain: persona.Brain{
			Provider: "ollama",
			Model:    "qwen3:30b",
			Worker:   persona.Worker{Provider: "grok", Model: "grok-4.6"},
		},
	}
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, p)
	m.prefillStack(p)

	got := m.formBrain().Worker
	if got.Provider != "grok" || got.Model != "grok-4.6" {
		t.Fatalf("worker round-trip = %+v, want grok/grok-4.6", got)
	}
}

// "(default)" is index 0 on every stack row, and it means inherit — the form
// must not invent a worker for a persona that never named one.
func TestPersonaFormWorkerDefaultsToInherit(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.prefillStack(nil)

	if got := m.formBrain().Worker; got.Provider != "" || got.Model != "" {
		t.Fatalf("a fresh form claimed a worker: %+v", got)
	}
	if m.workerModelLabel() != stackDefaultLabel {
		t.Fatalf("worker model row = %q, want %q", m.workerModelLabel(), stackDefaultLabel)
	}
}

// ←/→ cycles the Worker row like every other stack row.
func TestPersonaFormCyclesTheWorkerRow(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.prefillStack(nil)
	m.formRow = formRowWorkerProvider

	m.cycleStackRow(1)
	if got := m.formBrain().Worker.Provider; got != config.WorkerProviders[0] {
		t.Fatalf("→ on the Worker row = %q, want %q", got, config.WorkerProviders[0])
	}
	m.cycleStackRow(-1)
	if got := m.formBrain().Worker.Provider; got != "" {
		t.Fatalf("← back to (default) = %q, want inherit", got)
	}
}

// Switching the worker backend drops a pin the new one cannot load — the same
// rule the brain model row follows.
func TestPersonaFormWorkerBackendSwitchDropsAStaleModel(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.prefillStack(nil)
	m.applyBrainModels(personaModelsMsg{seq: m.brainModels.seq, providers: fakeScan(map[string][]string{
		"ollama": {"qwen3:8b"},
		"grok":   {"grok-4.6"},
	})})
	m.workerProviderIdx = providerIndex(stackWorkerProviders(), "ollama")
	m.workerModel = "qwen3:8b"
	m.formRow = formRowWorkerProvider

	// ollama → grok: qwen3:8b is not a grok model.
	m.cycleStackRow(1)
	if m.workerModel != "" {
		t.Fatalf("stale worker model survived the backend switch: %q", m.workerModel)
	}
}

// A pending scan reports "unknown", not "empty". Clamping on unknown would
// wipe a saved model the scan simply had not returned yet.
func TestPersonaFormWorkerKeepsModelWhileTheScanIsPending(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.prefillStack(nil)
	m.workerProviderIdx = providerIndex(stackWorkerProviders(), "ollama")
	m.workerModel = "qwen3:8b"

	m.clampSelectedWorkerModel()

	if m.workerModel != "qwen3:8b" {
		t.Fatalf("pending scan wiped the worker model: %q", m.workerModel)
	}
}

// The rows have to be on screen and reachable, or the choice may as well not
// exist — the same discoverability bar the brain rows are held to.
func TestPersonaFormShowsTheWorkerRows(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.formMode = "create"
	m.prefillStack(nil)

	view := stripANSI(m.View())
	if !strings.Contains(view, "Worker") || !strings.Contains(view, "Worker model") {
		t.Fatalf("form is missing the worker rows:\n%s", view)
	}
	// An app with no worker chosen says so here rather than implying one.
	if !strings.Contains(view, "Settings has no worker yet") {
		t.Fatalf("form did not disclose the unset app worker:\n%s", view)
	}
}

// Arrow-only navigation must pass through the worker rows on the way down.
func TestPersonaFormArrowsReachTheWorkerRows(t *testing.T) {
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama"}, nil)
	m.formMode = "create"
	m.prefillStack(nil)
	m.setFormRow(formRowBrainModel)

	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	if m.formRow != formRowWorkerProvider {
		t.Fatalf("↓ from Brain model = %d, want Worker %d", m.formRow, formRowWorkerProvider)
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	if m.formRow != formRowWorkerModel {
		t.Fatalf("↓ from Worker = %d, want Worker model %d", m.formRow, formRowWorkerModel)
	}
}

// Exercise the form's real create/edit writers, then read the YAML back and
// resolve the runtime config. Stubbed save callbacks missed a dropped Worker.
func TestPersonaWorkerFormPersistsCreationAndEdit(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "global-worker"}
	m := newPersonas(cfg)
	m.width, m.height = 100, 35
	m.beginCreate()
	m.nameInput.SetValue("Worker fixture")
	m.promptTA.SetValue("You are a helpful guide.")
	m.workerProviderIdx = providerIndex(stackWorkerProviders(), "codex")
	m.workerModel = "fixture-codex"
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" {
		t.Fatalf("create failed: %s", m.message)
	}
	check := func(provider, model, host string) {
		t.Helper()
		p, err := persona.Load("worker-fixture")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Brain.Worker; got.Provider != provider || got.Model != model || got.OllamaHost != host {
			t.Fatalf("saved worker = %+v", got)
		}
		effective := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "global-worker"}
		persona.Apply(effective, p)
		if config.VoiceWorkerProviderOf(effective) != provider || effective.VoiceWorkerModel != model {
			t.Fatalf("runtime worker = %s/%s", config.VoiceWorkerProviderOf(effective), effective.VoiceWorkerModel)
		}
	}
	check("codex", "fixture-codex", "")
	m.beginEditAt(formRowWorkerProvider)
	m.workerProviderIdx = providerIndex(stackWorkerProviders(), "ollama")
	m.workerModel, m.workerOllamaHost = "fixture-local", "http://worker:11434"
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" {
		t.Fatalf("edit failed: %s", m.message)
	}
	check("ollama", "fixture-local", "http://worker:11434")
	m.beginEditAt(formRowName)
	m.nameInput.SetValue("Renamed worker fixture")
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlJ})
	check("ollama", "fixture-local", "http://worker:11434")
}

func TestPersonaFormPreservesTierOverridesAndReportsOwnership(t *testing.T) {
	enabled := true
	p := &persona.Profile{ID: "jarvis", DisplayName: "Jarvis", Brain: persona.Brain{WorkerTiers: &config.WorkerTierOverrides{Simple: &config.WorkerTierOverride{Enabled: &enabled}}}}
	w := config.DefaultVoiceWorkers()
	w.Mode = config.WorkerModeTiered
	m := newPickerPersonas(t, &config.Config{BrainProvider: "ollama", VoiceWorkers: w}, p)
	m.prefillStack(p)
	got := m.formBrain().WorkerTiers
	if got == nil || got.Simple.Enabled == nil || !*got.Simple.Enabled {
		t.Fatal("editor discarded override")
	}
	*got.Simple.Enabled = false
	if !*p.Brain.WorkerTiers.Simple.Enabled || !*m.workerTiers.Simple.Enabled {
		t.Fatal("editor shares profile or returned patch")
	}
	m.formMode = "edit"
	view := stripANSI(m.View())
	if !strings.Contains(view, "persona overrides + Settings") || !strings.Contains(view, "legacy selection") {
		t.Fatalf("ownership missing from rendered form: %s", view)
	}
	m.formRow = formRowWorkerProvider
	if !strings.Contains(m.formRowHelp(), "brain.worker_tiers") {
		t.Fatal("UI claims legacy worker applies in tiered mode")
	}
}
