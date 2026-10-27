package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestWorkersControlsManagedDelegationWithoutExperimentalInbox(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime, err := delegation.New(t.Context(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	live := &brain.OllamaBrain{}
	if err := live.AttachDelegation(runtime); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Events: events.NewBus(), Brain: live}
	table := job.NewTable(t.Context())
	a := NewAgency(p, table, nil, &config.Config{VoiceWorkersDisabled: true}, nil)
	a.Wire()
	if _, err := runtime.Start("blocked"); !errors.Is(err, job.ErrDisabled) {
		t.Fatalf("legacy managed admission = %v", err)
	}
	p.SetWorkersEnabled(true)
	ticket, err := runtime.Start("legacy managed task")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("legacy managed task did not start")
	}
	p.SetWorkersEnabled(false)
	if items := p.Workers(); len(items) != 1 || items[0].ID != ticket || !items[0].Active() {
		t.Fatalf("managed task invisible: %+v", items)
	}
	if err := p.CancelWork(ticket); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy managed cancellation missed runtime")
	}
}

func TestWorkerDetailsSurviveCompletion(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	table := job.NewTable(t.Context())
	t.Cleanup(table.AbortAll)
	a := NewAgency(p, table, nil, &config.Config{}, nil)
	a.Wire()
	p.incActiveTurn() // Hold narration; this test inspects worker state only.
	started, release := make(chan struct{}), make(chan struct{})
	_, err := table.Start(t.Context(), job.Spec{Task: "Run checks", Provider: "codex", Model: "fixture-codex", Run: func(ctx context.Context) (string, error) {
		start, end := a.foldingToolHooks(job.IDFromContext(ctx))
		call := brain.ToolCall{ID: "check", Name: "commandExecution", Kind: brain.KindExecute, Summary: "go test ./..."}
		start(call)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		end(brain.ToolResult{ToolCall: call, Outcome: &events.ToolOutcome{Status: events.ToolOutcomeFailed, Reason: "exit 1", OutputPreview: "test failed", Elapsed: time.Second}})
		return "One check failed.", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	items := p.Workers()
	if len(items) != 1 || len(items[0].Tools) != 1 || items[0].Tools[0].Status != "running" || items[0].Tools[0].Summary != "go test ./..." || items[0].Provider != "codex" || items[0].Model != "fixture-codex" {
		t.Fatalf("running worker = %+v", items)
	}
	items[0].Tools[0].Summary = "mutated snapshot"
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		items = p.Workers()
		if len(items) == 1 && !items[0].Active() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not finish: %+v", items)
		}
		time.Sleep(time.Millisecond)
	}
	if len(items[0].Tools) != 1 || items[0].Tools[0].Status != "failed" || items[0].Tools[0].Preview != "test failed" || items[0].Tools[0].Summary != "go test ./..." || items[0].Result != "One check failed." {
		t.Fatalf("completed worker = %+v", items)
	}
}

func TestWorkersControlLegacyAndCanonicalAdmissions(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		cfg := &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "original-model", VoiceWorkersDisabled: true}
		p := &Pipeline{Events: events.NewBus(), Brain: &lifecycleWorker{reply: "live"}}
		jobs := job.NewTable(t.Context())
		t.Cleanup(jobs.AbortAll)
		a := NewAgency(p, jobs, &lifecycleWorker{reply: "worker result"}, cfg, nil)
		a.Wire()
		if canonical {
			if err := p.EnableConversationWork("workers", nil, cfg); err != nil {
				t.Fatal(err)
			}
		}
		a.handleSpawn(t.Context(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "inspect the source"})
		if p.WorkersEnabled() || len(p.Workers()) != 0 {
			t.Fatal("disabled worker admitted a task")
		}
		p.SetWorkersEnabled(true)
		a.handleSpawn(t.Context(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "inspect the source"})
		deadline := time.Now().Add(2 * time.Second)
		for {
			items := p.Workers()
			if len(items) == 1 && !items[0].Active() {
				if items[0].Provider != "ollama" || items[0].Model != "original-model" || items[0].Result != "worker result" {
					t.Fatalf("worker snapshot = %+v", items[0])
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("worker did not finish: %+v", items)
			}
			time.Sleep(time.Millisecond)
		}
		p.SetWorkersEnabled(false)
		a.ReplaceWorker(&lifecycleWorker{reply: "new"}, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "new-model"}, nil)
		if p.WorkersEnabled() || p.Workers()[0].Model != "original-model" {
			t.Fatal("replacement changed permission or previous job metadata")
		}
	}
}

func TestWorkerModelHandoffUsesPinnedModelInStatusAndFrozenAdmission(t *testing.T) {
	tests := []struct {
		name      string
		canonical bool
		cfg       config.Config
		provider  string
		model     string
	}{
		{
			name:     "inherited claude",
			cfg:      config.Config{BrainProvider: "claude", VoiceWorkerModel: "claude-sonnet"},
			provider: "claude",
			model:    "claude-sonnet",
		},
		{
			name:     "explicit claude",
			cfg:      config.Config{VoiceWorkerProvider: "claude", VoiceWorkerModel: "claude-opus"},
			provider: "claude",
			model:    "claude-opus",
		},
		{
			name:     "grok",
			cfg:      config.Config{VoiceWorkerProvider: "grok", VoiceWorkerModel: "grok-4"},
			provider: "grok",
			model:    "grok-4",
		},
		{
			name:      "ollama",
			canonical: true,
			cfg:       config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "qwen3:8b"},
			provider:  "ollama",
			model:     "qwen3:8b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			p := &Pipeline{Events: events.NewBus(), Brain: &lifecycleWorker{reply: "live"}}
			jobs := job.NewTable(t.Context())
			t.Cleanup(jobs.Close)
			a := NewAgency(p, jobs, &lifecycleWorker{reply: "worker result"}, &cfg, nil)
			a.Wire()
			if tt.canonical {
				if err := p.EnableConversationWork("model-handoff", nil, &cfg); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.CloseWork() })
			}

			a.handleSpawn(t.Context(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "inspect the source"})
			if tt.canonical {
				scope := p.WorkSnapshot().Scope
				deadline := time.Now().Add(2 * time.Second)
				var pending []work.ResultView
				for time.Now().Before(deadline) {
					pending = p.WorkCoordinator().Pending(scope)
					if len(pending) >= 1 {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if len(pending) == 0 {
					t.Fatalf("frozen admission did not finish: jobs=%+v snapshot=%+v", jobs.Status(), p.WorkSnapshot())
				}
				result := pending[0].Outcome
				if result.Provider != tt.provider || result.Model != tt.model || result.Config["model"] != tt.model {
					t.Fatalf("frozen worker provenance = %+v, want provider=%q model=%q", result, tt.provider, tt.model)
				}
				return
			}

			deadline := time.Now().Add(2 * time.Second)
			for {
				items := jobs.Status()
				if len(items) == 1 && items[0].State != job.StateQueued && items[0].State != job.StateRunning {
					if items[0].Provider != tt.provider || items[0].Model != tt.model {
						t.Fatalf("job status = %+v, want provider=%q model=%q", items[0], tt.provider, tt.model)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("job did not finish: %+v", items)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestWorkersStopAllIncludesManagedOwnerAfterPersonaReplacement(t *testing.T) {
	makeRuntime := func() (*delegation.Runtime, *managedHarnessClient) {
		client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
		runtime, err := delegation.New(t.Context(), client, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runtime.Close() })
		return runtime, client
	}
	first, client := makeRuntime()
	next, _ := makeRuntime()
	oldBrain := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: first, closed: make(chan struct{})}
	nextBrain := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: next, closed: make(chan struct{})}
	cfg := &config.Config{VoiceWorkerProvider: "ollama", DelegationProfile: "original-profile"}
	p := &Pipeline{Events: events.NewBus(), Brain: oldBrain}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.AbortAll)
	a := NewAgency(p, jobs, &lifecycleWorker{}, cfg, nil)
	a.Wire()
	if err := p.EnableConversationWork("workers", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	ticket, err := first.Start("managed task")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("managed job did not start")
	}
	p.SetWorkersEnabled(false)
	if err := p.ReplaceConversationBrain(nextBrain, &lifecycleWorker{}, &config.Config{VoiceWorkerProvider: "ollama", DelegationProfile: "next-profile"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := next.Start("must not start"); !errors.Is(err, job.ErrDisabled) {
		t.Fatalf("replacement admission = %v", err)
	}
	items := p.Workers()
	if len(items) != 1 || items[0].ID != ticket || items[0].Model != "original-profile" || !items[0].Active() {
		t.Fatalf("retained work = %+v", items)
	}
	if err := p.StopWorkers(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("stop all missed the old managed owner")
	}
	waitWorkPending(t, p.WorkCoordinator(), p.WorkSnapshot().Scope, 1)
	items = p.Workers()
	if len(items) != 1 || items[0].Active() || items[0].Termination != "confirmed" {
		t.Fatalf("terminal managed snapshot = %+v", items)
	}
}

func TestWorkersStopAllCancelsLegacyRunningAndQueued(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	jobs := job.NewTable(t.Context())
	a := NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	started := make(chan struct{})
	id, err := jobs.Start(t.Context(), job.Spec{Task: "running", Run: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	a.work.Add(events.ToolCallStarted{JobID: id, ID: "read", Kind: "read"})
	a.work.Add(events.ToolCallStarted{JobID: id, ID: "run", Kind: "execute"})
	if activity := p.Workers()[0].Activity; !strings.Contains(activity, "command") || strings.Contains(activity, "file") {
		t.Fatalf("expected latest activity, got %q", activity)
	}
	_, err = jobs.Start(t.Context(), job.Spec{Task: "queued", Run: func(context.Context) (string, error) { t.Error("queued job ran"); return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	p.SetWorkersEnabled(false)
	if err := p.StopWorkers(); err != nil {
		t.Fatal(err)
	}
	for _, item := range p.Workers() {
		if item.Active() {
			t.Fatalf("active after stop all: %+v", item)
		}
	}
	if p.WorkersEnabled() {
		t.Fatal("stop all unexpectedly enabled admission")
	}
}
