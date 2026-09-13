package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestWorkerSaveCanDisablePaidTierWithoutDispatchAvailability(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	account := "default"
	cfg.VoiceWorkers.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "claude", Model: "sonnet", Billing: config.WorkerBillingSubscription, AccountRef: &account}
	cfg.VoiceWorkers.Policy.PaidEnabled = true
	committed, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = committed
	m := newSettings(cfg, nil)
	m.workerDraft = cfg.VoiceWorkers.Clone()
	m.workerOpenRevision = committed.Revision
	m.workerDraft.Policy.PaidEnabled = false
	if cmd := m.saveWorkerDraft(); cmd == nil {
		t.Fatal("paid disable was not saved")
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Policy.PaidEnabled || loaded.VoiceWorkers.Tiers.Simple.Assignment.Provider != "claude" {
		t.Fatalf("saved=%+v", loaded.VoiceWorkers)
	}
}

func TestWorkerSaveCanPersistStrictBlockedPolicy(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	account := "default"
	cfg.VoiceWorkers.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "grok", Model: "grok-3", Billing: config.WorkerBillingMetered, AccountRef: &account}
	cfg.VoiceWorkers.Policy.PaidEnabled = true
	cfg.VoiceWorkers.Policy.ChargePolicy = "strict"
	daily := int64(100)
	cfg.VoiceWorkers.Policy.DailyChargeMicroUSD = &daily
	committed, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = committed
	m := newSettings(cfg, nil)
	m.workerDraft, m.workerOpenRevision = cfg.VoiceWorkers.Clone(), committed.Revision
	if cmd := m.saveWorkerDraft(); cmd == nil {
		t.Fatal("strict policy was not persisted")
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil || loaded.VoiceWorkers.Policy.ChargePolicy != "strict" || loaded.VoiceWorkers.Policy.DailyChargeMicroUSD == nil {
		t.Fatalf("loaded=%+v err=%v", loaded.VoiceWorkers, err)
	}
	if reason := workerTierUnavailable(loaded.VoiceWorkers, config.WorkerTierSimple); !strings.Contains(reason, "strict charge cap unavailable") {
		t.Fatalf("availability=%q", reason)
	}
}

func TestWorkerSaveRejectsMalformedProviderWithoutChange(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	committed, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = committed
	m := newSettings(cfg, nil)
	m.workerDraft, m.workerOpenRevision = cfg.VoiceWorkers.Clone(), committed.Revision
	m.workerDraft.Tiers.Simple.Assignment.Provider = "unknown"
	if cmd := m.saveWorkerDraft(); cmd != nil {
		t.Fatal("malformed provider returned save command")
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil || loaded.VoiceWorkers.Revision != committed.Revision || loaded.VoiceWorkers.Tiers.Simple.Assignment.Provider != "ollama" {
		t.Fatalf("loaded=%+v err=%v", loaded.VoiceWorkers, err)
	}
}
