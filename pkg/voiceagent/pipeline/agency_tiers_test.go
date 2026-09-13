package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func tierTestConfig() *config.Config {
	w := config.DefaultVoiceWorkers()
	w.Mode, w.Revision = config.WorkerModeTiered, 7
	host := "http://127.0.0.1:11434"
	w.Tiers.Simple.Enabled = true
	w.Tiers.Simple.Revision = 3
	w.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "first:7b", EndpointRef: &host, Billing: config.WorkerBillingLocal}
	return &config.Config{BrainProvider: "ollama", ActivePersona: "samantha", VoiceWorkers: w}
}

func TestTieredQueuePinsPlanAcrossSettingsAndPersonaReplacement(t *testing.T) {
	cfg := tierTestConfig()
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(1))
	defer jobs.Close()
	p := &Pipeline{Events: events.NewBus()}
	a := NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	if err := a.EnableConversationWork("shared-chat", nil, cfg, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.CloseWork(); err != nil {
			t.Error(err)
		}
	}()
	originalScope := a.WorkSnapshot().Scope
	block := make(chan struct{})
	started := make(chan struct{})
	if _, err := jobs.Start(context.Background(), job.Spec{Task: "occupy slot", Run: func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-block:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}); err != nil {
		t.Fatal(err)
	}
	<-started
	plans := make(chan *config.WorkerPlan, 2)
	origins := make(chan string, 2)
	a.planFactory = func(_ context.Context, c *config.Config, plan *config.WorkerPlan) (brain.Provider, error) {
		plans <- plan
		origins <- c.ActivePersona
		return &lifecycleWorker{reply: "finished"}, nil
	}
	spawn := func(label string) {
		metrics := newTurnMetrics()
		a.HandleRoute(context.Background(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Tier: config.WorkerTierSimple, Transcript: label})
	}
	spawn("inspect the first task")
	if len(plans) != 0 {
		t.Fatal("queued job constructed its provider before dispatch")
	}
	saved := a.WorkSnapshot()
	if len(saved.Jobs) != 1 || saved.Jobs[0].Config["worker_revision"] != "7" || saved.Jobs[0].Config["worker_tier"] != "simple" {
		t.Fatalf("persisted admission lacks pinned plan: %+v", saved.Jobs)
	}
	next := cfg.VoiceWorkers.Clone()
	next.Revision, next.Tiers.Simple.Revision = 8, 4
	next.Tiers.Simple.Assignment.Model = "second:8b"
	if err := a.ApplyWorkerSettings(next); err != nil {
		t.Fatal(err)
	}
	// The prepared persona still carries revision 7. It cannot restore that
	// policy over the Settings commit, and it cannot relabel the queued job.
	stalePersona := cfg.Clone()
	stalePersona.ActivePersona = "santa"
	a.ReplaceWorker(nil, stalePersona, nil)
	if a.WorkSnapshot().Scope != originalScope {
		t.Fatal("persona replacement changed conversation scope")
	}
	spawn("inspect the second task")
	close(block)
	for i, want := range []string{"first:7b", "second:8b"} {
		select {
		case plan := <-plans:
			if plan.Assignment.Model != want || plan.ConfigRevision != int64(7+i) {
				t.Fatalf("plan %d = %+v", i, plan)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued work did not dispatch")
		}
		wantPersona := []string{"samantha", "santa"}[i]
		if got := <-origins; got != wantPersona {
			t.Fatalf("origin %q, want %q", got, wantPersona)
		}
	}
}

func TestTieredAdmissionRejectsMissingDisabledAndPaidAssignments(t *testing.T) {
	for _, scenario := range []string{"missing tier", "disabled", "paid", "spoofed local"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := tierTestConfig()
			tier := config.WorkerTierSimple
			switch scenario {
			case "missing tier":
				tier = ""
			case "disabled":
				cfg.VoiceWorkers.Tiers.Simple.Enabled = false
			case "paid":
				cfg.VoiceWorkers.Tiers.Simple.Assignment.Billing = config.WorkerBillingMetered
			case "spoofed local":
				cfg.VoiceWorkers.Tiers.Simple.Assignment.Provider = "grok"
			}
			jobs := job.NewTable(context.Background())
			defer jobs.Close()
			p := &Pipeline{Events: events.NewBus()}
			a := NewAgency(p, jobs, &lifecycleWorker{}, cfg, nil)
			a.Wire()
			a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
				t.Error("invalid assignment dispatched")
				return nil, errors.New("unexpected")
			}
			metrics := newTurnMetrics()
			a.HandleRoute(context.Background(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Tier: tier, Transcript: "inspect these changes"})
			if len(jobs.Status()) != 0 {
				t.Fatal("invalid assignment admitted a job")
			}
		})
	}
}

func TestWorkerSettingsPublicationClonesAndRejectsStaleEditors(t *testing.T) {
	a := NewAgency(nil, nil, nil, tierTestConfig(), nil)
	next := a.WorkerSettings()
	next.Revision++
	if err := a.ApplyWorkerSettings(next); err != nil {
		t.Fatal(err)
	}
	next.Tiers.Simple.Assignment.Model = "uncommitted"
	if a.WorkerSettings().Tiers.Simple.Assignment.Model == "uncommitted" {
		t.Fatal("published policy aliases draft")
	}
	if err := a.ApplyWorkerSettings(next); !errors.Is(err, config.ErrWorkerRevisionConflict) {
		t.Fatalf("conflicting same revision = %v", err)
	}
	if err := a.ApplyWorkerSettings(tierTestConfig().VoiceWorkers); !errors.Is(err, config.ErrWorkerRevisionConflict) {
		t.Fatalf("stale revision = %v", err)
	}
}
