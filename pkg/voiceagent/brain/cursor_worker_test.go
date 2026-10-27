package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestCursorStrictPlanRejectedBeforeProviderConstruction(t *testing.T) {
	// Even a directly supplied plan must not bypass the config resolver's
	// strict-policy rejection. No Cursor installation is needed for rejection.
	t.Setenv("CURSOR_AGENT_BIN", filepath.Join(t.TempDir(), "missing-cursor"))
	plan := &config.WorkerPlan{
		Assignment: config.WorkerAssignment{Provider: "cursor", Model: "auto", Billing: config.WorkerBillingSubscription, AccountRef: strptr("default")},
		Policy:     config.WorkerPolicy{ChargePolicy: "strict"},
	}
	if err := ValidateWorkerPlan(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "strict worker policy") {
		t.Fatalf("strict plan validation: %v", err)
	}
	if worker, err := NewPlannedWorker(t.Context(), &config.Config{}, plan); worker != nil || err == nil || !strings.Contains(err.Error(), "strict worker policy") {
		t.Fatalf("strict plan construction: worker=%T err=%v", worker, err)
	}
}

func TestCursorStrictBudgetNeverInvokesCLI(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			runner := &fakeCursorClient{}
			b := newTestCursorBrain(runner)
			b.sessionID = "existing-session"
			b.history = []Turn{{Role: "user", Content: "existing history"}}
			before := append([]Turn(nil), b.history...)
			opts := WorkerStreamOptions(tools)
			opts.WorkerBudget = NewWorkerBudget(BudgetLimits{StrictTools: true})
			var err error
			if streaming {
				var stream *Stream
				stream, err = b.ThinkStream(t.Context(), "inspect the files", opts)
				if stream != nil {
					t.Fatal("strict worker started a stream")
				}
			} else {
				_, err = b.ThinkFull(t.Context(), "inspect the files", opts)
			}
			if !errors.Is(err, ErrWorkerBudgetExceeded) || len(runner.prompts) != 0 {
				t.Fatalf("stream=%t tools=%t: error=%v CLI calls=%d", streaming, tools, err, len(runner.prompts))
			}
			if !reflect.DeepEqual(b.history, before) || b.sessionID != "existing-session" {
				t.Fatal("strict rejection changed conversation state")
			}
		}
	}
}

func TestEveryBrainCanBeAWorker(t *testing.T) {
	for _, p := range Providers() {
		if !config.WorkerProviderAllowed(p.Name) {
			t.Errorf("brain %s missing from worker choices", p.Name)
		}
	}
}

func TestCursorWorkerFactories(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "cursor-agent")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_AGENT_BIN", binary)
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "cursor", VoiceWorkerModel: "worker-model", CursorModel: "chat-model", WorkDir: t.TempDir(), PromptsDir: t.TempDir()}
	legacy, err := NewWorkerProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan := &config.WorkerPlan{Assignment: config.WorkerAssignment{Provider: "cursor", Model: "pinned-model", Billing: config.WorkerBillingSubscription, AccountRef: strptr("default")}}
	planned, err := NewPlannedWorker(t.Context(), cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		worker Provider
		model  string
	}{{legacy, "worker-model"}, {planned, "pinned-model"}} {
		b, ok := tc.worker.(*CursorBrain)
		if !ok {
			t.Fatalf("worker = %T", tc.worker)
		}
		if !b.cfg.IsWorker || b.cfg.CursorModel != tc.model || b.cfg.WorkDir != cfg.WorkDir {
			t.Fatalf("wrong worker configuration: %+v", b.cfg)
		}
	}
	if cfg.CursorModel != "chat-model" || cfg.IsWorker {
		t.Fatal("worker construction mutated the live config")
	}
}

func TestCursorWorkerFreshJobsDoNotResumeEachOther(t *testing.T) {
	fake := &fakeCursorClient{results: []*cursor.AskResult{{Result: "one", SessionID: "job-one"}, {Result: "two", SessionID: "job-two"}}}
	b := newTestCursorBrain(fake)
	b.cfg.IsWorker = true
	b.cfg.CursorModel = "worker-model"
	b.sessionID, b.hostContext, b.promptTokens = "live-session", "private-host-context", 99
	b.history = []Turn{{Role: "user", Content: "private-history"}}
	for _, task := range []string{"first job", "second job"} {
		fresh := b.Fresh().(*CursorBrain)
		if fresh.sessionID != "" || fresh.hostContext != "" || fresh.promptTokens != 0 || len(fresh.history) != 0 {
			t.Fatal("fresh worker inherited conversation state")
		}
		if _, err := fresh.ThinkFull(t.Context(), task, WorkerStreamOptions(true)); err != nil {
			t.Fatal(err)
		}
	}
	for i, opts := range fake.opts {
		if opts.Resume != "" || opts.Mode != cursor.ModeUnset || opts.Model != "worker-model" || opts.Force || opts.Yolo {
			t.Fatalf("job %d options: %+v", i, opts)
		}
		for _, unwanted := range []string{"private-history", "private-host-context", "Reply now."} {
			if strings.Contains(fake.prompts[i], unwanted) {
				t.Fatalf("job %d leaked %q", i, unwanted)
			}
		}
	}
	if b.sessionID != "live-session" || len(b.history) != 1 {
		t.Fatal("fresh jobs modified their parent")
	}
}

type canceledCursorWorker struct{ calls int }

func (c *canceledCursorWorker) AskCtx(ctx context.Context, _ string, _ *cursor.AskOptions) (*cursor.AskResult, error) {
	c.calls++
	return nil, ctx.Err()
}
func TestCursorWorkerCancellationDoesNotRetry(t *testing.T) {
	runner := &canceledCursorWorker{}
	b := newTestCursorBrain(runner)
	b.sessionID = "existing-job"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := b.ThinkFull(ctx, "canceled job", WorkerStreamOptions(true))
	if !errors.Is(err, context.Canceled) || runner.calls != 1 || len(b.history) != 0 {
		t.Fatalf("cancellation: err=%v attempts=%d history=%v", err, runner.calls, b.history)
	}
}

func TestCursorWorkerTierRequiresExplicitPaidPolicy(t *testing.T) {
	w := config.DefaultVoiceWorkers()
	w.Mode = config.WorkerModeTiered
	w.Tiers.Simple.Enabled = true
	w.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "cursor", Model: "auto", Billing: config.WorkerBillingSubscription, AccountRef: strptr("default")}
	cfg := &config.Config{VoiceWorkers: w}
	resolve := func(want string) {
		t.Helper()
		_, err := config.ResolveWorkerPlan(cfg, config.WorkerTierSimple, LocalWorkerCapabilities{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	resolve("paid worker dispatch is disabled")
	w.Policy.PaidEnabled = true
	resolve("allow_uncapped")
	w.Tiers.Simple.AllowUncapped = true
	if _, err := config.ResolveWorkerPlan(cfg, config.WorkerTierSimple, LocalWorkerCapabilities{}); err != nil {
		t.Fatal(err)
	}
	limit := int64(1000000)
	w.Policy.DailyChargeMicroUSD = &limit
	w.Policy.ChargePolicy = "strict"
	resolve("strict charge cap unavailable")
	w.Policy.ChargePolicy = "advisory"
	w.Tiers.Simple.Assignment.Billing = config.WorkerBillingLocal
	resolve("cannot use local billing")
}
