package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/usage"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkerAllowanceActionReadsUnknownReservation(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	cfg.VoiceWorkers.Policy.PaidEnabled = true
	if _, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0); err != nil {
		t.Fatal(err)
	}
	l, err := usage.Open(config.WorkerLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := cfg.VoiceWorkers.Policy
	ctx := context.Background()
	if err := l.SyncPolicy(ctx, usage.Policy{ID: config.WorkerPolicyID, Revision: cfg.VoiceWorkers.Revision, PaidEnabled: p.PaidEnabled, Timezone: p.Timezone}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, usage.Request{ID: "unknown-fixture", Account: "claude:default", PolicyID: config.WorkerPolicyID, PolicyRevision: cfg.VoiceWorkers.Revision, AllowUncapped: true, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m := newSettings(cfg, nil)
	m.section = sectionWorker
	var row settingRow
	for _, candidate := range m.workerRows() {
		if candidate.key == "worker_allowance" {
			row = candidate
			break
		}
	}
	if row.run == nil {
		t.Fatal("allowance row is not actionable")
	}
	cmd := row.run(&m)
	if cmd == nil {
		t.Fatal("allowance action returned no command")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	if next.workerAllowance == nil || next.workerAllowance.UnknownCount != 1 {
		t.Fatalf("allowance=%+v", next.workerAllowance)
	}
	if next.workerAllowanceValue() == "inspect" {
		t.Fatal("allowance value did not update")
	}
}

func TestWorkerAllowanceTerminalInspectionShowsUnknownCharge(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	cfg.VoiceWorkers.Policy.PaidEnabled = true
	committed, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = committed
	l, err := usage.Open(config.WorkerLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.SyncPolicy(ctx, usage.Policy{ID: config.WorkerPolicyID, Revision: committed.Revision, PaidEnabled: true, Timezone: committed.Policy.Timezone}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, usage.Request{ID: "terminal-unknown", Account: "claude:default", PolicyID: config.WorkerPolicyID, PolicyRevision: committed.Revision, AllowUncapped: true, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: "ollama", Available: true}})
	m.section, m.width, m.height = sectionWorker, 160, 45
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 8), saved: make(chan *config.VoiceWorkers, 1)}
	programCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(programCtx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, runErr := p.Run(); done <- runErr }()
	defer func() { p.Quit(); <-done }()
	key := func(msg tea.KeyMsg) workerTerminalState {
		p.Send(msg)
		select {
		case state := <-h.states:
			return state
		case <-programCtx.Done():
			t.Fatal("terminal key timed out")
			return workerTerminalState{}
		}
	}
	state := key(tea.KeyMsg{Type: tea.KeyHome})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && state.key != "worker_allowance" {
		state = key(tea.KeyMsg{Type: tea.KeyDown})
	}
	if state.key != "worker_allowance" {
		t.Fatalf("focused %q", state.key)
	}
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.output.Lock()
		rendered := h.text.String()
		h.output.Unlock()
		if strings.Contains(rendered, "unknown 1") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.output.Lock()
	rendered := h.text.String()
	h.output.Unlock()
	if !strings.Contains(rendered, "unknown 1") || !strings.Contains(rendered, "reserved 0") || !strings.Contains(rendered, "settled 0") {
		t.Fatalf("terminal output missing allowance state: %s", rendered)
	}
	if strings.Contains(rendered, "remaining 0") || strings.Contains(rendered, "remaining 100") {
		t.Fatalf("terminal output claimed hard remaining allowance: %s", rendered)
	}
}

func TestWorkerAllowanceRefreshFailureClearsStaleSnapshot(t *testing.T) {
	m := newSettings(tieredSettingsFixture(), nil)
	known := int64(7)
	m, _ = m.Update(workerAllowanceMsg{snapshot: usage.Snapshot{ReservedMicroUSD: &known, UnknownCount: 1}})
	if m.workerAllowance == nil || m.workerAllowanceValue() == "inspect" {
		t.Fatal("successful refresh was not retained")
	}
	m, _ = m.Update(workerAllowanceMsg{err: context.DeadlineExceeded})
	if m.workerAllowance != nil || m.workerAllowanceValue() != "unavailable" {
		t.Fatalf("failed refresh retained stale state: snapshot=%+v value=%q", m.workerAllowance, m.workerAllowanceValue())
	}
	if m.message == "" {
		t.Fatal("failed refresh did not surface message")
	}
	m, _ = m.Update(workerAllowanceMsg{snapshot: usage.Snapshot{UnknownCount: 0}})
	if m.workerAllowance == nil || m.workerAllowanceErr != "" || m.workerAllowanceValue() == "unavailable" {
		t.Fatalf("successful retry state=%+v err=%q value=%q", m.workerAllowance, m.workerAllowanceErr, m.workerAllowanceValue())
	}
}
