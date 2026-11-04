package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPersonaWorkerTierInheritanceAndClone(t *testing.T) {
	base := validTierWorkers()
	original := base.Clone()
	disabled := false
	assignment := *base.Tiers.Simple.Assignment
	assignment.Model = "persona-model"
	cfg := &Config{VoiceWorkers: base, PersonaWorkerTiers: &WorkerTierOverrides{Simple: &WorkerTierOverride{Assignment: &assignment}, Medium: &WorkerTierOverride{Enabled: &disabled}}}
	plan, err := ResolveWorkerPlan(cfg, WorkerTierSimple, &testWorkerCaps{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Assignment.Model != "persona-model" || plan.Limits != base.Tiers.Simple.Limits {
		t.Fatalf("merge: %+v", plan)
	}
	if _, err := ResolveWorkerPlan(cfg, WorkerTierMedium, &testWorkerCaps{}); err == nil {
		t.Fatal("explicit disable ignored")
	}
	if !reflect.DeepEqual(base, original) {
		t.Fatal("global defaults mutated")
	}
	clone := cfg.Clone()
	clone.PersonaWorkerTiers.Simple.Assignment.Model = "changed"
	*clone.PersonaWorkerTiers.Medium.Enabled = true
	if cfg.PersonaWorkerTiers.Simple.Assignment.Model != "persona-model" || *cfg.PersonaWorkerTiers.Medium.Enabled {
		t.Fatal("patch shared across clone")
	}
	cfg.PersonaWorkerTiers = nil
	inherited, err := ResolveWorkerPlan(cfg, WorkerTierSimple, &testWorkerCaps{})
	if err != nil || inherited.Assignment.Model != base.Tiers.Simple.Assignment.Model {
		t.Fatalf("clear did not inherit: %v", err)
	}
	if plan.Assignment.Model != "persona-model" {
		t.Fatal("admitted plan mutated")
	}
}

func TestPersonaWorkersCannotOverridePaidInstallationPolicy(t *testing.T) {
	w := validTierWorkers()
	yes := true
	account := "default"
	cfg := &Config{VoiceWorkers: w, PersonaWorkerTiers: &WorkerTierOverrides{Simple: &WorkerTierOverride{Assignment: &WorkerAssignment{Provider: "codex", Model: "persona-paid", Billing: WorkerBillingSubscription, AccountRef: &account}, AllowUncapped: &yes}}}
	if _, err := ResolveWorkerPlan(cfg, WorkerTierSimple, &testWorkerCaps{}); err == nil || !strings.Contains(err.Error(), "paid worker dispatch is disabled") {
		t.Fatalf("paid global disable bypassed: %v", err)
	}
	var unsupported WorkerTierOverrides
	if err := json.Unmarshal([]byte(`{"policy":{"paid_enabled":true}}`), &unsupported); err == nil {
		t.Fatal("JSON accepted persona global policy override")
	}
	for _, input := range []string{"policy: {paid_enabled: true}", "simple: {concurrent_workers: 9}", "simple: {limits: {runtime_seconds: -1}}", "simple: {assignment: {provider: ollama, model: x, billing: local, secret: x}}"} {
		var overrides WorkerTierOverrides
		if err := yaml.Unmarshal([]byte(input), &overrides); err == nil {
			t.Fatalf("accepted unsupported patch: %s", input)
		}
	}
}
