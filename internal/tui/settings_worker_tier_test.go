package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func tieredSettingsFixture() *config.Config {
	w := config.DefaultVoiceWorkers()
	w.Mode = config.WorkerModeTiered
	w.Tiers.Simple.Enabled = true
	w.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "qwen3:8b", Billing: config.WorkerBillingLocal}
	return &config.Config{VoiceWorkers: w}
}

func TestTierWorkerDraftCancelLeavesCommittedConfig(t *testing.T) {
	cfg := tieredSettingsFixture()
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: "ollama", Available: true, Models: []string{"qwen3:8b", "qwen3:30b"}}})
	m.section = sectionWorker
	m.ensureWorkerDraft().Tiers.Simple.Assignment.Model = "qwen3:30b"
	m.cancelWorkerDraft()
	if cfg.VoiceWorkers.Tiers.Simple.Assignment.Model != "qwen3:8b" {
		t.Fatal("cancel changed committed assignment")
	}
	if !strings.Contains(m.message, "cancel") {
		t.Fatalf("message=%q", m.message)
	}
}

func TestTierWorkerFailedSaveKeepsDraftAndConfig(t *testing.T) {
	cfg := tieredSettingsFixture()
	m := newSettings(cfg, nil)
	m.workerDraft = cfg.VoiceWorkers.Clone()
	m.workerDraft.Tiers.Simple.Assignment.Model = "qwen3:30b"
	m.saveWorkers = func(*config.VoiceWorkers, int64) (*config.VoiceWorkers, error) { return nil, errors.New("conflict") }
	if cmd := m.saveWorkerDraft(); cmd != nil {
		t.Fatal("failed save returned success message")
	}
	if m.workerDraft.Tiers.Simple.Assignment.Model != "qwen3:30b" {
		t.Fatal("failed save discarded draft")
	}
	if cfg.VoiceWorkers.Tiers.Simple.Assignment.Model != "qwen3:8b" {
		t.Fatal("failed save changed config")
	}
	if !strings.Contains(m.message, "conflict") {
		t.Fatalf("message=%q", m.message)
	}
}

func TestTierWorkerSaveUsesCASAndPersistsRevision(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	cfg := tieredSettingsFixture()
	if err := config.SaveVoiceWorkersCAS(cfg.VoiceWorkers, 0); err != nil {
		t.Fatal(err)
	}
	m := newSettings(cfg, nil)
	m.workerDraft = cfg.VoiceWorkers.Clone()
	m.workerOpenRevision = 1
	m.workerDraft.Tiers.Simple.Assignment.Model = "qwen3:30b"
	cmd := m.saveWorkerDraft()
	if cmd == nil {
		t.Fatal("save did not return completion message")
	}
	msg := cmd()
	if _, ok := msg.(workerSettingsSavedMsg); !ok {
		t.Fatalf("message=%T", msg)
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Revision != 2 || loaded.VoiceWorkers.Tiers.Simple.Assignment.Model != "qwen3:30b" {
		t.Fatalf("persisted=%+v", loaded.VoiceWorkers)
	}
	if _, err := os.Stat(config.ConfigFile()); err != nil {
		t.Fatal(err)
	}
}

func TestTierWorkerRowsShowDisabledUnconfiguredAssignments(t *testing.T) {
	cfg := tieredSettingsFixture()
	m := newSettings(cfg, nil)
	m.section = sectionWorker
	m.height = 80
	view := stripANSI(m.View())
	for _, want := range []string{"Simple", "Medium", "Complex", "Disabled", "Save worker settings", "Cancel worker edits"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}
