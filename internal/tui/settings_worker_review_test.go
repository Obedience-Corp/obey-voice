package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func workerReviewKey(t *testing.T, m *settingsModel, key tea.KeyType) tea.Cmd {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: key})
	*m = next
	return cmd
}

func workerReviewNavigate(t *testing.T, m *settingsModel, want string) {
	t.Helper()
	workerReviewKey(t, m, tea.KeyHome)
	for i := 0; i < 100; i++ {
		row, _, _ := m.focused()
		if row.key == want {
			return
		}
		workerReviewKey(t, m, tea.KeyDown)
	}
	t.Fatalf("cannot reach %s", want)
}

func TestWorkerMigrationKeyboardRequiresBillingBeforePersistence(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "ollama", OllamaModel: "qwen3:8b", OllamaHost: "http://localhost:11434"}
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: "ollama", Available: true, Models: []string{"qwen3:8b"}}})
	m.section, m.width, m.height = sectionWorker, 180, 55
	workerReviewNavigate(t, &m, "worker_migration")
	workerReviewKey(t, &m, tea.KeyEnter)
	workerReviewNavigate(t, &m, "worker_save")
	if cmd := workerReviewKey(t, &m, tea.KeyEnter); cmd != nil {
		t.Fatal("unlabelled migration saved")
	}
	if !strings.Contains(m.message, "Simple") || !strings.Contains(m.message, "billing") {
		t.Fatalf("unhelpful refusal: %s", m.message)
	}
	if _, err := os.Stat(config.ConfigFile()); !os.IsNotExist(err) {
		t.Fatalf("failed Save wrote config: %v", err)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "unavailable") {
		t.Fatalf("migration shown as ready: %s", view)
	}
	for _, name := range []string{"simple", "medium", "complex"} {
		workerReviewNavigate(t, &m, "worker_"+name)
		workerReviewKey(t, &m, tea.KeyEnter)
		// Opening a tier also opens its assignment editor; retain its value.
		workerReviewKey(t, &m, tea.KeyEsc)
		workerReviewNavigate(t, &m, "worker_"+name+"_billing")
		workerReviewKey(t, &m, tea.KeyEnter) // unknown wraps to local
		workerReviewNavigate(t, &m, "worker_back")
		workerReviewKey(t, &m, tea.KeyEnter)
	}
	workerReviewNavigate(t, &m, "worker_save")
	if cmd := workerReviewKey(t, &m, tea.KeyEnter); cmd == nil {
		t.Fatalf("labelled migration failed: %s", m.message)
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex} {
		if _, err := config.ResolveWorkerPlan(loaded, name, brain.LocalWorkerCapabilities{}); err != nil {
			t.Fatalf("saved %s not resolvable: %v", name, err)
		}
	}
}

func TestWorkerDraftEditsPreserveExplicitChoices(t *testing.T) {
	m := newSettings(tieredSettingsFixture(), []discovery.ProviderInfo{{Name: "ollama", Available: true, Models: []string{"qwen3:30b"}}})
	d := m.ensureWorkerDraft()
	d.Tiers.Simple.Enabled = false
	d.Tiers.Simple.Assignment.Billing = config.WorkerBillingMetered
	m.commitWorkerAssignment("simple", "ollama/qwen3:30b")
	if d.Tiers.Simple.Enabled || d.Tiers.Simple.Assignment.Billing != config.WorkerBillingMetered {
		t.Fatal("assignment edit changed explicit enable/billing choice")
	}
	m.commitWorkerReference("medium", "endpoint reference", "http://localhost:11434")
	row := workerBillingRow(config.WorkerTierMedium, "Medium", nil)
	row.set(&m, 0)
	if d.Tiers.Medium.Assignment != nil {
		t.Fatal("reference/billing edit created incomplete assignment")
	}
	m.commitWorkerLimit("simple", "output_tokens", "42")
	if d.Tiers.Simple.Limits.OutputTokens == nil || *d.Tiers.Simple.Limits.OutputTokens != 42 {
		t.Fatal("output cap not set")
	}
	m.commitWorkerLimit("simple", "output_tokens", "uncapped")
	if d.Tiers.Simple.Limits.OutputTokens != nil {
		t.Fatal("output cap not cleared")
	}
	m.commitWorkerLimit("simple", "output_tokens", "42")
	m.commitWorkerLimit("simple", "output_tokens", "")
	if d.Tiers.Simple.Limits.OutputTokens != nil {
		t.Fatal("blank output cap not cleared")
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("editor left unsaveable document: %v", err)
	}
}

func TestWorkerSettingsShowCapabilityScopeAndExistingBounds(t *testing.T) {
	cfg := tieredSettingsFixture()
	cfg.VoiceWorkerMaxConcurrent = 2
	cfg.VoiceWorkerJobBudget = 75
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionWorker, 180, 60
	for _, tier := range []config.WorkerTierName{"", config.WorkerTierSimple} {
		m.workerEditingTier = tier
		view := stripANSI(m.View())
		wants := []string{"1 concurrent", "32 queued"}
		if tier != "" {
			wants = append(wants, "local request/tool/output quotas")
		}
		for _, want := range wants {
			if !strings.Contains(view, want) {
				t.Fatalf("%s view lacks %q: %s", tier, want, view)
			}
		}
	}
}

func TestWorkerCASConflictReloadsOnlyOnExplicitAction(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	saved, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = saved
	m := newSettings(cfg, nil)
	m.ensureWorkerDraft().Tiers.Simple.Assignment.Model = "my-edit"
	external := saved.Clone()
	external.Tiers.Simple.Assignment.Model = "external-edit"
	latest, err := config.CommitVoiceWorkersCAS(external, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cmd := m.saveWorkerDraft(); cmd != nil {
		t.Fatal("stale save succeeded")
	}
	if m.workerOpenRevision != 1 || m.workerDraft.Tiers.Simple.Assignment.Model != "my-edit" {
		t.Fatal("conflict silently rebased or lost edits")
	}
	if !strings.Contains(m.message, "Reload saved worker settings") {
		t.Fatalf("no recovery action: %s", m.message)
	}
	m.reloadWorkerDraft()
	if m.workerOpenRevision != latest.Revision || m.ensureWorkerDraft().Tiers.Simple.Assignment.Model != "external-edit" {
		t.Fatal("reload missed committed revision")
	}
	m.workerDraft.Tiers.Simple.Assignment.Model = "reapplied-edit"
	if cmd := m.saveWorkerDraft(); cmd == nil {
		t.Fatalf("save after reload failed: %s", m.message)
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Revision != 3 || loaded.VoiceWorkers.Tiers.Simple.Assignment.Model != "reapplied-edit" {
		t.Fatal("reapplied edit not persisted")
	}
}
