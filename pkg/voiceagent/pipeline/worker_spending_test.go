package pipeline

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/usage"
)

type spendingFake struct {
	calls  int
	err    error
	cancel context.CancelFunc
}

func (f *spendingFake) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (f *spendingFake) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	f.calls++
	if f.cancel != nil {
		f.cancel()
		return "", ctx.Err()
	}
	return "fake result", f.err
}
func (*spendingFake) ClearHistory()            {}
func (*spendingFake) History() []brain.Turn    { return nil }
func (*spendingFake) LoadHistory([]brain.Turn) {}

func spendingPlan() *config.WorkerPlan {
	account := "default"
	return &config.WorkerPlan{Tier: config.WorkerTierSimple, Assignment: config.WorkerAssignment{Provider: "claude", Model: "x", AccountRef: &account, Billing: config.WorkerBillingSubscription}, AllowUncapped: true, Policy: config.WorkerPolicy{PaidEnabled: true, ChargePolicy: "advisory"}}
}
func savedPaidConfig(t *testing.T) *config.Config {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	w := config.DefaultVoiceWorkers()
	w.Mode = config.WorkerModeTiered
	for _, tier := range []*config.WorkerTier{&w.Tiers.Simple, &w.Tiers.Medium, &w.Tiers.Complex} {
		tier.Enabled = true
	}
	w.Policy.PaidEnabled = true
	w.Policy.ChargePolicy = "advisory"
	for _, tier := range []*config.WorkerTier{&w.Tiers.Simple, &w.Tiers.Medium, &w.Tiers.Complex} {
		a := "default"
		tier.Assignment = &config.WorkerAssignment{Provider: "claude", Model: "x", AccountRef: &a, Billing: config.WorkerBillingSubscription}
		tier.AllowUncapped = true
	}
	if err := config.SaveVoiceWorkersCAS(w, 0); err != nil {
		t.Fatal(err)
	}
	return &config.Config{VoiceWorkers: w}
}

func TestPaidWorkerRunAdvisoryRetainsUncertain(t *testing.T) {
	cfg := savedPaidConfig(t)
	f := &spendingFake{}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, cfg, nil)
	if _, err := a.paidWorkerRun(context.Background(), "task", f, cfg, spendingPlan(), "req-1"); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
	l, err := usage.Open(config.WorkerLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s, err := l.Snapshot(context.Background(), "workers", time.Now())
	if err != nil || s.UnknownCount != 1 {
		t.Fatalf("snapshot=%+v err=%v", s, err)
	}
	replay, err := l.Reserve(context.Background(), usage.Request{ID: "req-1", Account: "claude:default", PolicyID: "workers", PolicyRevision: 1, AllowUncapped: true, Now: time.Now()})
	if err != nil || replay.Created || replay.State != "uncertain" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestPaidWorkerRunCanceledStillRecords(t *testing.T) {
	cfg := savedPaidConfig(t)
	f := &spendingFake{}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.paidWorkerRun(ctx, "task", f, cfg, spendingPlan(), "req-cancel"); err == nil {
		t.Fatal("canceled run succeeded")
	}
	if f.calls != 0 {
		t.Fatal("fake called after cancellation")
	}
}

func TestPaidWorkerRunCancellationAfterDispatchRetainsUncertain(t *testing.T) {
	cfg := savedPaidConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &spendingFake{cancel: cancel}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, cfg, nil)
	if _, err := a.paidWorkerRun(ctx, "task", f, cfg, spendingPlan(), "req-after-cancel"); err == nil {
		t.Fatal("canceled worker unexpectedly succeeded")
	}
	l, err := usage.Open(config.WorkerLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s, err := l.Snapshot(context.Background(), "workers", time.Now())
	if err != nil || s.UnknownCount != 1 {
		t.Fatalf("snapshot=%+v err=%v", s, err)
	}
}

func TestPaidWorkerRunBlocksPaidOffAndStrictPlansBeforeProvider(t *testing.T) {
	for _, policy := range []config.WorkerPolicy{{PaidEnabled: false, ChargePolicy: "advisory"}, {PaidEnabled: true, ChargePolicy: "strict"}} {
		t.Run(policy.ChargePolicy, func(t *testing.T) {
			cfg := savedPaidConfig(t)
			f := &spendingFake{}
			p := spendingPlan()
			p.Policy = policy
			jobs := job.NewTable(context.Background())
			defer jobs.Close()
			a := NewAgency(nil, jobs, nil, cfg, nil)
			if _, err := a.paidWorkerRun(context.Background(), "task", f, cfg, p, "blocked"); err == nil {
				t.Fatal("blocked plan succeeded")
			}
			if f.calls != 0 {
				t.Fatal("provider called for blocked plan")
			}
		})
	}
}

func TestPaidWorkerRunStorageFailureBlocksProvider(t *testing.T) {
	cfg := savedPaidConfig(t)
	if err := os.Mkdir(config.WorkerLedgerPath(), 0700); err != nil {
		t.Fatal(err)
	}
	f := &spendingFake{}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, cfg, nil)
	if _, err := a.paidWorkerRun(context.Background(), "task", f, cfg, spendingPlan(), "storage"); err == nil {
		t.Fatal("storage failure ignored")
	}
	if f.calls != 0 {
		t.Fatal("provider called after storage failure")
	}
}

func TestPaidWorkerRunReplayDoesNotCallProviderTwice(t *testing.T) {
	cfg := savedPaidConfig(t)
	f := &spendingFake{}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, cfg, nil)
	if _, err := a.paidWorkerRun(context.Background(), "task", f, cfg, spendingPlan(), "replay"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.paidWorkerRun(context.Background(), "task", f, cfg, spendingPlan(), "replay"); err == nil {
		t.Fatal("replay succeeded")
	}
	if f.calls != 1 {
		t.Fatalf("provider calls=%d", f.calls)
	}
}
