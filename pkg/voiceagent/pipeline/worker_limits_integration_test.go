package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type workerLimitsProbe struct {
	toolsEnabled bool
	requestErr   error
	toolErr      error
}

func (*workerLimitsProbe) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}

func (w *workerLimitsProbe) ThinkFull(ctx context.Context, _ string, opts brain.StreamOptions) (string, error) {
	w.toolsEnabled = opts.ToolsEnabled
	if opts.WorkerBudget == nil {
		return "", errors.New("worker budget missing")
	}
	if err := opts.WorkerBudget.AcquireRequest(ctx); err != nil {
		return "", err
	}
	// A completed request makes the next request and tool reservations use the
	// observed budget rather than the in-flight guard.
	opts.WorkerBudget.RecordUsage(1, 1)
	if err := opts.WorkerBudget.AcquireRequest(ctx); err == nil {
		return "", errors.New("second model request was admitted")
	} else {
		w.requestErr = err
	}
	if err := opts.WorkerBudget.AcquireTool(ctx); err != nil {
		return "", err
	}
	if err := opts.WorkerBudget.AcquireTool(ctx); err == nil {
		return "", errors.New("second tool action was admitted")
	} else {
		w.toolErr = err
	}
	return "bounded", nil
}

func (*workerLimitsProbe) ClearHistory()            {}
func (*workerLimitsProbe) History() []brain.Turn    { return nil }
func (*workerLimitsProbe) LoadHistory([]brain.Turn) {}

func TestPlannedWorkerRunPassesFrozenLimitsAndDeadlineToFactory(t *testing.T) {
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, &config.Config{}, nil)
	worker := &workerLimitsProbe{}
	factoryCalled := make(chan struct{}, 1)
	var factoryDeadline time.Time
	a.planFactory = func(ctx context.Context, _ *config.Config, got *config.WorkerPlan) (brain.Provider, error) {
		var ok bool
		factoryDeadline, ok = ctx.Deadline()
		if !ok {
			t.Error("planned worker factory context has no runtime deadline")
		}
		if got.Limits.ModelRequests != 1 || got.Limits.ToolCalls != 1 || got.Limits.OutputTokens == nil || *got.Limits.OutputTokens != 4 {
			t.Errorf("factory plan limits = %+v, want frozen request/tool/output limits", got.Limits)
		}
		factoryCalled <- struct{}{}
		return worker, nil
	}
	workerCfg := &config.Config{VoiceWorkers: config.DefaultVoiceWorkers()}
	workerCfg.VoiceWorkers.Mode = config.WorkerModeTiered
	outTokens := 4
	plan := &config.WorkerPlan{
		Tier:       config.WorkerTierSimple,
		Assignment: config.WorkerAssignment{Provider: "ollama", Model: "local", Billing: config.WorkerBillingLocal},
		Limits:     config.WorkerLimits{RuntimeSeconds: 1, ModelRequests: 1, ToolCalls: 1, OutputTokens: &outTokens},
		Policy:     config.WorkerPolicy{ChargePolicy: "advisory"},
	}
	started := time.Now()
	got, err := a.plannedWorkerRun("bounded task", workerCfg, plan)(context.Background())
	if err != nil || got != "bounded" {
		t.Fatalf("planned worker result = %q, error = %v", got, err)
	}
	select {
	case <-factoryCalled:
	case <-time.After(time.Second):
		t.Fatal("planned worker factory was not called")
	}
	if remaining := time.Until(factoryDeadline); remaining < 700*time.Millisecond || remaining > 1200*time.Millisecond {
		t.Fatalf("factory deadline remaining = %v, want approximately one second from entry", remaining)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("bounded worker unexpectedly consumed its whole runtime budget")
	}
	if !worker.toolsEnabled {
		t.Fatal("tiered planned worker did not receive its full tool surface")
	}
	if !errors.Is(worker.requestErr, brain.ErrWorkerBudgetExceeded) {
		t.Fatalf("second request error = %v, want worker budget exceeded", worker.requestErr)
	}
	if !errors.Is(worker.toolErr, brain.ErrWorkerBudgetExceeded) {
		t.Fatalf("second tool error = %v, want worker budget exceeded", worker.toolErr)
	}
}
