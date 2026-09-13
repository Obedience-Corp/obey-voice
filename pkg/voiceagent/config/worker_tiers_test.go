package config

import (
	"errors"
	"os"
	"testing"
)

type testWorkerCaps struct {
	err    error
	got    WorkerAssignment
	report WorkerCapabilities
}

func (c *testWorkerCaps) ValidateWorkerAssignment(a WorkerAssignment) error      { c.got = a; return c.err }
func (c *testWorkerCaps) WorkerCapabilities(WorkerAssignment) WorkerCapabilities { return c.report }

func validTierWorkers() *VoiceWorkers {
	w := DefaultVoiceWorkers()
	w.Mode = WorkerModeTiered
	for _, t := range []*WorkerTier{&w.Tiers.Simple, &w.Tiers.Medium, &w.Tiers.Complex} {
		t.Enabled = true
		t.Assignment = &WorkerAssignment{Provider: "ollama", Model: "local:7b", Billing: WorkerBillingLocal}
	}
	return w
}

func TestWorkerPlanRequiresTieredAndClones(t *testing.T) {
	w := validTierWorkers()
	cfg := &Config{VoiceWorkers: w}
	caps := &testWorkerCaps{}
	p, err := ResolveWorkerPlan(cfg, WorkerTierSimple, caps)
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigRevision != w.Revision || p.AllowUncapped {
		t.Fatal("plan metadata missing")
	}
	if p.Assignment.EndpointRef != nil {
		t.Fatal("unexpected endpoint")
	}
	w.Tiers.Simple.Assignment.Provider = "changed"
	if p.Assignment.Provider == "changed" {
		t.Fatal("plan aliases config")
	}
	w.Mode = WorkerModeLegacy
	if _, err := ResolveWorkerPlan(cfg, WorkerTierSimple, caps); err == nil {
		t.Fatal("legacy mode resolved")
	}
}

func TestWorkerPlanRejectsSpoofedBillingThroughTrustedCapabilities(t *testing.T) {
	w := validTierWorkers()
	w.Tiers.Simple.Assignment.Billing = WorkerBillingMetered
	caps := &testWorkerCaps{err: errors.New("billing label not trusted")}
	if _, err := ResolveWorkerPlan(&Config{VoiceWorkers: w}, WorkerTierSimple, caps); err == nil {
		t.Fatal("spoofed paid billing resolved")
	}
}

func TestWorkerPlanPaidRequiresExplicitOptInAndAccount(t *testing.T) {
	w := validTierWorkers()
	w.Tiers.Simple.Assignment = &WorkerAssignment{Provider: "claude", Model: "sonnet", Billing: WorkerBillingSubscription}
	cfg := &Config{VoiceWorkers: w}
	caps := &testWorkerCaps{}
	if _, err := ResolveWorkerPlan(cfg, WorkerTierSimple, caps); err == nil {
		t.Fatal("paid worker bypassed disabled policy")
	}
	w.Policy.PaidEnabled = true
	w.Tiers.Simple.AllowUncapped = true
	if _, err := ResolveWorkerPlan(cfg, WorkerTierSimple, caps); err == nil {
		t.Fatal("paid worker without account resolved")
	}
	a := "default"
	w.Tiers.Simple.Assignment.AccountRef = &a
	p, err := ResolveWorkerPlan(cfg, WorkerTierSimple, caps)
	if err != nil {
		t.Fatal(err)
	}
	if p.Assignment.AccountRef == nil || *p.Assignment.AccountRef != "default" {
		t.Fatalf("account=%v", p.Assignment.AccountRef)
	}
}

func TestWorkerPlanStrictRejectsUnboundedPaidAdapter(t *testing.T) {
	w := validTierWorkers()
	a := "default"
	w.Tiers.Simple.Assignment = &WorkerAssignment{Provider: "grok", Model: "grok-3", Billing: WorkerBillingMetered, AccountRef: &a}
	w.Policy.PaidEnabled = true
	w.Policy.ChargePolicy = "strict"
	w.Tiers.Simple.AllowUncapped = true
	if _, err := ResolveWorkerPlan(&Config{VoiceWorkers: w}, WorkerTierSimple, &testWorkerCaps{}); err == nil {
		t.Fatal("strict unbounded adapter resolved")
	}
}

func TestSaveVoiceWorkersCASRoundTripAndConflict(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	w := validTierWorkers()
	if err := SaveVoiceWorkersCAS(w, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers == nil || loaded.VoiceWorkers.Revision != 1 {
		t.Fatalf("roundtrip revision: %#v", loaded.VoiceWorkers)
	}
	if err := SaveVoiceWorkersCAS(w, 0); !errors.Is(err, ErrWorkerRevisionConflict) {
		t.Fatalf("stale save error=%v", err)
	}
	if _, err := SetKeyFile("voice_worker_job_budget", "9"); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers == nil || loaded.VoiceWorkers.Revision != 1 {
		t.Fatal("unrelated edit clobbered tiers")
	}
	second := validTierWorkers()
	if _, err := CommitVoiceWorkersCAS(second, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadWithoutHooks()
	if err != nil || loaded.VoiceWorkers == nil || loaded.VoiceWorkers.Revision != 2 {
		t.Fatalf("fresh disk revision not observed: %#v %v", loaded.VoiceWorkers, err)
	}
}

func TestPreviewLegacyUsesEffectivePinnedModel(t *testing.T) {
	cfg := &Config{BrainProvider: "ollama", OllamaModel: "voice", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker", OllamaHost: "http://localhost:11434"}
	w, err := PreviewLegacyWorkerMigration(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w.Tiers.Simple.Assignment.Model != "worker" {
		t.Fatalf("model=%q", w.Tiers.Simple.Assignment.Model)
	}
	if w.Tiers.Simple.Assignment.Billing != WorkerBillingUnknown {
		t.Fatal("migration guessed billing")
	}
}

func TestLoadRejectsMissingRequiredWorkerFields(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	if err := os.WriteFile(ConfigFile(), []byte("voice_workers:\n  schema_version: 1\n  revision: 0\n  mode: tiered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithoutHooks(); err == nil {
		t.Fatal("missing tiers accepted")
	}
}

func TestSaveVoiceWorkersWriteFailureLeavesSource(t *testing.T) {
	root := t.TempDir()
	blocker := root + "/blocked"
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	SetConfigDirForTest(t, blocker)
	if err := SaveVoiceWorkersCAS(validTierWorkers(), 0); err == nil {
		t.Fatal("expected write failure")
	}
}

func TestWorkerSchemaRejectsUnknownAndTimezone(t *testing.T) {
	w := validTierWorkers()
	w.Policy.Timezone = "Mars/Olympus"
	if err := w.Validate(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	SetConfigDirForTest(t, t.TempDir())
	w = validTierWorkers()
	if err := SaveVoiceWorkersCAS(w, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, []byte("\nvoice_workers:\n  unexpected: true\n")...)
	if err := os.WriteFile(ConfigFile(), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithoutHooks(); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestWorkerTierRevisionOverflow(t *testing.T) {
	w := validTierWorkers()
	old := cloneTier(w.Tiers.Simple)
	old.Revision = 1<<63 - 1
	w.Tiers.Simple.Assignment.Provider = "changed"
	if _, err := nextTierRevision(old, w.Tiers.Simple); err == nil {
		t.Fatal("tier revision overflow accepted")
	}
}
