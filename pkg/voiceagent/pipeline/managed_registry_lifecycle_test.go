package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestManagedRegistryPrunesIdleReplacementsButRetainsRunningOwner(t *testing.T) {
	clientOriginal := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtimeOriginal, err := delegation.New(context.Background(), clientOriginal, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	brainOriginal := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "original"}, runtime: runtimeOriginal, closed: make(chan struct{})}

	clientMiddle := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtimeMiddle, err := delegation.New(context.Background(), clientMiddle, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	brainMiddle := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "middle"}, runtime: runtimeMiddle, closed: make(chan struct{})}

	clientCurrent := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtimeCurrent, err := delegation.New(context.Background(), clientCurrent, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	brainCurrent := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "current"}, runtime: runtimeCurrent, closed: make(chan struct{})}

	p := &Pipeline{Events: events.NewBus(), Brain: brainOriginal}
	jobs := job.NewTable(context.Background())
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	if err := p.EnableConversationWork("registry-session", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeOriginal.Start("original running"); err != nil {
		t.Fatal(err)
	}
	originalRequest := <-clientOriginal.started

	if err := p.ReplaceConversationBrain(brainMiddle, &lifecycleWorker{reply: "middle"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil); err != nil {
		t.Fatal(err)
	}
	if !runtimeOriginal.Enabled() {
		t.Fatal("running original runtime was closed by first replacement")
	}
	if !runtimeMiddle.Enabled() {
		t.Fatal("idle middle runtime was closed before becoming current")
	}

	if err := p.ReplaceConversationBrain(brainCurrent, &lifecycleWorker{reply: "current"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil); err != nil {
		t.Fatal(err)
	}
	if !runtimeOriginal.Enabled() {
		t.Fatal("running original runtime was closed by second replacement")
	}
	if runtimeMiddle.Enabled() {
		t.Fatal("idle middle runtime was not stopped")
	}
	assertManagedRegistryEventually(t, a, func(reg map[*delegation.Runtime]struct{}) bool {
		_, original := reg[runtimeOriginal]
		_, middle := reg[runtimeMiddle]
		_, current := reg[runtimeCurrent]
		return original && !middle && current
	})

	clientOriginal.result <- delegation.Snapshot{ID: originalRequest.Key, State: "completed", Text: "original complete"}
	waitWorkPending(t, p.WorkCoordinator(), p.WorkSnapshot().Scope, 1)
	if runtimeOriginal.Enabled() {
		t.Fatal("completed original runtime remained enabled")
	}
	assertManagedRegistryEventually(t, a, func(reg map[*delegation.Runtime]struct{}) bool {
		_, original := reg[runtimeOriginal]
		_, current := reg[runtimeCurrent]
		return !original && current
	})
	if !runtimeCurrent.Enabled() {
		t.Fatal("current runtime was pruned after original completion")
	}

	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if runtimeCurrent.Enabled() {
		t.Fatal("final CloseWork left current runtime enabled")
	}
}

func assertManagedRegistryEventually(t *testing.T, a *Agency, want func(map[*delegation.Runtime]struct{}) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.workMu.RLock()
		reg := make(map[*delegation.Runtime]struct{}, len(a.managed))
		for runtime := range a.managed {
			reg[runtime] = struct{}{}
		}
		a.workMu.RUnlock()
		if want(reg) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	a.workMu.RLock()
	defer a.workMu.RUnlock()
	t.Fatalf("managed registry did not converge: current=%p managed=%d", a.managedCurrent, len(a.managed))
}
