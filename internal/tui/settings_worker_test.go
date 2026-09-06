package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func workerTestProviders() []discovery.ProviderInfo {
	return []discovery.ProviderInfo{
		{Name: "ollama", Available: true, Models: []string{"qwen3:8b", "qwen3:30b"}},
		{Name: "grok", Available: true, Models: []string{"grok-4.6"}},
		{Name: "claude", Available: true},
		{Name: "cursor", Available: true, Models: []string{"auto"}},
	}
}

func newWorkerSettings(t *testing.T, cfg *config.Config) settingsModel {
	t.Helper()
	m := newSettings(cfg, workerTestProviders())
	m.section = sectionWorker
	m.cursor = 0
	return m
}

// Cursor can talk but cannot run a background job, so it must never be offered
// as a worker — a row Apply cannot honour is worse than a missing row.
func TestWorkerSectionOffersOnlyWorkerBackends(t *testing.T) {
	m := newWorkerSettings(t, &config.Config{BrainProvider: "ollama"})
	var got []string
	for _, item := range m.workerItems {
		if item.kind == workerRowProvider {
			got = append(got, item.value)
		}
	}
	if strings.Join(got, ",") != "ollama,grok,claude" {
		t.Fatalf("worker providers = %v, want ollama,grok,claude", got)
	}
}

// The whole point of the change: picking a brain does not quietly leave the
// worker to a default. Settings lands the operator on the choice.
func TestChoosingABrainLandsOnTheWorkerSection(t *testing.T) {
	cfg := &config.Config{BrainProvider: "grok"}
	saved := map[string]any{}
	m := newSettings(cfg, workerTestProviders())
	m.saveConfig = func(k string, v any) error { saved[k] = v; return nil }
	m.section = sectionProvider
	m.cursor = 0 // ollama

	m.selectCurrent()

	if m.section != sectionWorker {
		t.Fatalf("section after choosing a brain = %d, want the Worker section %d", m.section, sectionWorker)
	}
	if !strings.Contains(m.message, "choose the worker") {
		t.Fatalf("message did not ask for a worker: %q", m.message)
	}
}

// Once a worker is chosen, picking a different brain must not drag the operator
// back through a decision they already made.
func TestChoosingABrainKeepsAnExistingWorkerChoice(t *testing.T) {
	cfg := &config.Config{BrainProvider: "grok", VoiceWorkerProvider: "ollama"}
	m := newSettings(cfg, workerTestProviders())
	m.saveConfig = func(string, any) error { return nil }
	m.section = sectionProvider
	m.cursor = 0

	m.selectCurrent()

	if m.section != sectionProvider {
		t.Fatalf("section = %d, want to stay on Provider %d", m.section, sectionProvider)
	}
}

func TestSelectingAWorkerPersistsItAndDropsAStaleModel(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerModel: "qwen3:30b"}
	saved := map[string]any{}
	m := newWorkerSettings(t, cfg)
	m.saveConfig = func(k string, v any) error { saved[k] = v; return nil }
	m.cursor = 1 // grok

	m.selectWorkerItem()

	if saved["voice_worker_provider"] != "grok" || cfg.VoiceWorkerProvider != "grok" {
		t.Fatalf("worker not saved: saved=%v cfg=%q", saved["voice_worker_provider"], cfg.VoiceWorkerProvider)
	}
	// An ollama tag means nothing to grok; keeping it would pin a model that
	// backend cannot load.
	if saved["voice_worker_model"] != "" || cfg.VoiceWorkerModel != "" {
		t.Fatalf("stale model survived the backend switch: %q", cfg.VoiceWorkerModel)
	}
}

func TestSelectingAWorkerModelPinsIt(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama"}
	saved := map[string]any{}
	m := newWorkerSettings(t, cfg)
	m.saveConfig = func(k string, v any) error { saved[k] = v; return nil }

	// Rows are the three providers, a divider, "default", then the catalog.
	m.cursor = len(config.WorkerProviders) + 2
	m.selectWorkerItem()

	if cfg.VoiceWorkerModel != "qwen3:8b" || saved["voice_worker_model"] != "qwen3:8b" {
		t.Fatalf("worker model = %q / saved %v, want qwen3:8b", cfg.VoiceWorkerModel, saved["voice_worker_model"])
	}

	// "default" clears the pin rather than writing the literal word.
	m.cursor = len(config.WorkerProviders) + 1
	m.selectWorkerItem()
	if cfg.VoiceWorkerModel != "" {
		t.Fatalf("default did not clear the pin: %q", cfg.VoiceWorkerModel)
	}
}

// An unset worker still runs something. The status line names what, so the
// fallback is visible instead of silent.
func TestWorkerStatusNamesTheInheritedBackend(t *testing.T) {
	m := newWorkerSettings(t, &config.Config{BrainProvider: "ollama"})
	status := m.workerStatus()
	if !strings.Contains(status, "not set") || !strings.Contains(status, "ollama") {
		t.Fatalf("status = %q, want it to say not set and name ollama", status)
	}
	if m.activeWorkerProvider() != "" {
		t.Fatalf("an unchosen worker must not render as chosen: %q", m.activeWorkerProvider())
	}

	m.cfg.VoiceWorkerProvider = "grok"
	if got := m.workerStatus(); strings.Contains(got, "not set") {
		t.Fatalf("status still says not set after a choice: %q", got)
	}
}

// The Worker section must survive a render at the default terminal size with
// its rows intact — it is reached by tabbing, and a blank tab reads as broken.
func TestWorkerSectionRenders(t *testing.T) {
	m := newWorkerSettings(t, &config.Config{BrainProvider: "ollama"})
	view := stripANSI(m.View())
	for _, want := range []string{"Worker", "ollama", "grok", "claude", "Worker model", "default"} {
		if !strings.Contains(view, want) {
			t.Errorf("worker view missing %q:\n%s", want, view)
		}
	}
}

func TestWorkerSectionIgnoresUnavailableBackends(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama"}
	providers := []discovery.ProviderInfo{
		{Name: "ollama", Available: true, Models: []string{"qwen3:8b"}},
		{Name: "grok", Available: false},
	}
	m := newSettings(cfg, providers)
	m.section = sectionWorker
	m.saveConfig = func(string, any) error { return nil }
	m.cursor = 1 // grok, not installed

	m.selectWorkerItem()

	if cfg.VoiceWorkerProvider != "" {
		t.Fatalf("selected an unavailable worker: %q", cfg.VoiceWorkerProvider)
	}
	if !strings.Contains(m.message, "not available") {
		t.Fatalf("message = %q, want it to say the backend is not available", m.message)
	}
}

// Tab must reach the Worker section from the far side too, so the section is
// not a dead end when cycling backwards.
func TestShiftTabReachesWorkerFromTools(t *testing.T) {
	m := settingsModel{cfg: &config.Config{}, section: sectionTools}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.section != sectionWorker {
		t.Fatalf("section after Tools + shift-tab = %d, want Worker %d", m.section, sectionWorker)
	}
}
