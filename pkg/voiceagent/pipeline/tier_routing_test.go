package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestTierRoutesCrossHTTPBrainPipelineAndAdmission(t *testing.T) {
	for _, tier := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex, ""} {
		t.Run(string(tier), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprint(w, `{"models":[{"name":"fixture-model"}]}`)
					return
				}
				if r.URL.Path != "/api/chat" {
					http.NotFound(w, r)
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				tools, _ := json.Marshal(request["tools"])
				for _, private := range []string{"fixture-model", serverPrivateModel} {
					if strings.Contains(string(tools), private) {
						t.Errorf("private label leaked into routing tools: %s", tools)
					}
				}
				if !strings.Contains(string(tools), `"tier"`) {
					t.Error("live request missing tier schema")
				}
				response := map[string]any{"model": "fixture-model", "done": true, "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "route", "arguments": map[string]any{"kind": "spawn", "tier": tier, "reason_code": "dependent_steps"}}}}}}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			cfg := tierTestConfig()
			cfg.OllamaHost, cfg.OllamaModel, cfg.MaxHistory = server.URL, "fixture-model", 20
			cfg.PromptsDir, cfg.WorkDir = t.TempDir(), t.TempDir()
			cfg.VoiceWorkers.Tiers.Simple.Assignment.Model = serverPrivateModel
			cfg.VoiceWorkers.Tiers.Simple.Assignment.EndpointRef = &server.URL
			clone := cfg.VoiceWorkers.Clone()
			cfg.VoiceWorkers.Tiers.Medium, cfg.VoiceWorkers.Tiers.Complex = clone.Tiers.Simple, clone.Clone().Tiers.Simple
			live, err := brain.NewOllama(cfg)
			if err != nil {
				t.Fatal(err)
			}
			bus := events.NewBus()
			jobs := job.NewTable(context.Background())
			defer jobs.Close()
			p := &Pipeline{Brain: live, Events: bus, VoiceToolsEnabled: true}
			a := NewAgency(p, jobs, nil, cfg, nil)
			a.Wire()
			plans := make(chan *config.WorkerPlan, 1)
			a.planFactory = func(_ context.Context, _ *config.Config, plan *config.WorkerPlan) (brain.Provider, error) {
				plans <- plan
				return &lifecycleWorker{reply: "done"}, nil
			}
			var mu sync.Mutex
			var route events.RouteDecided
			events.Subscribe(bus, func(e events.RouteDecided) { mu.Lock(); route = e; mu.Unlock() })
			if err := p.RunTurnTextMode(context.Background(), "search the web for the requested evidence"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got := route
			mu.Unlock()
			if tier == "" {
				if got.State != "rejected" || len(jobs.Status()) != 0 {
					t.Fatalf("missing tier did not reject: %+v", got)
				}
				return
			}
			if got.State != "accepted" || got.Tier != string(tier) || got.ReasonCode != "dependent_steps" || got.JobID == "" {
				t.Fatalf("route lost metadata: %+v", got)
			}
			select {
			case plan := <-plans:
				if plan.Tier != tier || plan.Assignment.Model != serverPrivateModel {
					t.Fatalf("wrong plan %+v", plan)
				}
			case <-time.After(time.Second):
				t.Fatal("no worker dispatched")
			}
		})
	}
}

const serverPrivateModel = "private-worker-model"

func TestAcceptedRoutePrecedesWorkerAndSpeech(t *testing.T) {
	cfg := tierTestConfig()
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Events: events.NewBus(), TTS: &progressiveFakeTTS{}, Player: player}
	a := NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	var mu sync.Mutex
	accepted := false
	events.Subscribe(p.Events, func(e events.RouteDecided) {
		if e.State == "accepted" {
			if len(jobs.Status()) != 1 {
				t.Error("accepted event precedes actual queue admission")
			}
			mu.Lock()
			accepted = true
			mu.Unlock()
		}
	})
	check := func() {
		mu.Lock()
		defer mu.Unlock()
		if !accepted {
			t.Error("work or acknowledgement preceded visible admission")
		}
	}
	events.Subscribe(p.Events, func(events.SpeakingStarted) { check() })
	started := make(chan struct{})
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		check()
		close(started)
		return &lifecycleWorker{reply: "done"}, nil
	}
	metrics := newTurnMetrics()
	a.HandleRoute(context.Background(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Tier: config.WorkerTierSimple, Transcript: "inspect this configuration"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
}

func TestTieredFollowupsReferenceExistingJobs(t *testing.T) {
	for _, input := range []string{"try again", "retry", "any update", "hurry up", "do it now", "is it done yet"} {
		kind, _, ok := classifyTurn(input, RouteContext{JobActive: true, TieredWorkers: true})
		if !ok || kind != RouteJobStatus {
			t.Errorf("%q = %s, expected existing job status", input, kind)
		}
	}
	if kind, _, ok := classifyTurn("search the web for evidence", RouteContext{TieredWorkers: true}); ok && kind == RouteSpawn {
		t.Fatal("tiered work skipped model tier classification")
	}
}

func TestQueuedPaidPlanChecksCurrentDisableBeforeFactory(t *testing.T) {
	cfg := tierTestConfig()
	cfg.VoiceWorkers.Policy.PaidEnabled = true
	a := NewAgency(&Pipeline{}, job.NewTable(context.Background()), nil, cfg, nil)
	defer a.jobs.Close()
	plan := &config.WorkerPlan{Assignment: config.WorkerAssignment{Provider: "grok", Model: "paid", Billing: config.WorkerBillingMetered}, Policy: cfg.VoiceWorkers.Policy}
	run := a.plannedWorkerRun("task", cfg, plan)
	latest := cfg.VoiceWorkers.Clone()
	latest.Revision++
	latest.Policy.PaidEnabled = false
	if err := a.ApplyWorkerSettings(latest); err != nil {
		t.Fatal(err)
	}
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		t.Error("paid factory invoked")
		return nil, nil
	}
	if _, err := run(context.Background()); !errors.Is(err, ErrWorkerPolicyChanged) {
		t.Fatalf("dispatch error %v", err)
	}
}
