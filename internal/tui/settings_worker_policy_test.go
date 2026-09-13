package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestWorkerCapabilityScopeLabels(t *testing.T) {
	local := "ollama"
	cli := "claude"
	for _, tc := range []struct {
		name, provider, want string
	}{
		{"local", local, "local request/tool/output quotas"},
		{"cli", cli, "CLI request/tool/output quotas unavailable"},
		{"disabled", "", "provider not assigned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var assignment *config.WorkerAssignment
			if tc.provider != "" {
				assignment = &config.WorkerAssignment{Provider: tc.provider}
			}
			got, _ := workerCapabilityScope(assignment)
			if got != tc.want {
				t.Fatalf("scope=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkerPolicyDetailsStayTruthful(t *testing.T) {
	cfg := tieredSettingsFixture()
	m := newSettings(cfg, nil)
	m.section, m.workerEditingTier = sectionWorker, ""
	rows := m.tierWorkerRows()
	var charge, budget, timezone, allowance settingRow
	for _, row := range rows {
		switch row.key {
		case "worker_charge_policy":
			charge = row
		case "worker_daily_budget":
			budget = row
		case "worker_timezone":
			timezone = row
		case "worker_allowance":
			allowance = row
		}
	}
	if strings.Contains(charge.detail, "not yet enforced") || strings.Contains(budget.detail, "not yet enforced") || strings.Contains(timezone.detail, "not yet enforced") {
		t.Fatalf("stale policy detail: %+v %+v %+v", charge, budget, timezone)
	}
	if !strings.Contains(allowance.detail, "unknown charges remain unknown") {
		t.Fatalf("allowance=%+v", allowance)
	}
}
