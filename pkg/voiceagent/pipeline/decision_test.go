package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestOptionalDecisionCrossesPipelineAndWorkerAdmission(t *testing.T) {
	for _, mode := range []string{"text", "voice", "off", "early", "late", "error", "unavailable", "settings", "disable", "history", "persona", "conversation", "rewritten", "background", "control", "continuation", "host-closed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var modelCalls, decisionCalls, workers atomic.Int32
			liveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
					return
				}
				modelCalls.Add(1)
				var req struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if len(req.Tools) > 0 {
					fmt.Fprint(w, `{"done":true,"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]}}`)
					return
				}
				fmt.Fprint(w, `{"done":true,"message":{"role":"assistant","content":"Let us discuss it."}}`)
			}))
			defer liveServer.Close()
			entered := make(chan struct{})
			release := make(chan struct{})
			decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				decisionCalls.Add(1)
				close(entered)
				if mode == "late" {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				if mode == "error" {
					w.WriteHeader(503)
					return
				}
				tier := "simple"
				if mode == "unavailable" {
					tier = "complex"
				}
				one := 1.0
				probs := map[string]float64{"none": 0, "simple": 0, "medium": 0, "complex": 0}
				probs[tier] = 1
				_ = json.NewEncoder(w).Encode(decision.Response{Answers: map[string]decision.Answer{"work": {Noul: &one}, "tier": {Choice: tier, Confidence: &one, Probabilities: probs}}})
			}))
			defer decisionServer.Close()
			defer close(release)
			cfg := tierTestConfig()
			cfg.OllamaHost, cfg.OllamaModel = liveServer.URL, "fixture"
			cfg.PromptsDir, cfg.WorkDir = t.TempDir(), t.TempDir()
			b, err := brain.NewOllama(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			const input = "Check the installed Git version."
			p := &Pipeline{Brain: b, STT: &fakeSTT{text: input}, Events: events.NewBus()}
			p.SetOutputMuted(true)
			if err := p.BeginConversationIdentity("decision-test"); err != nil {
				t.Fatal(err)
			}
			jobs := job.NewTable(t.Context())
			defer jobs.Close()
			a := NewAgency(p, jobs, nil, cfg, nil)
			a.Wire()
			a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
				workers.Add(1)
				return &lifecycleWorker{reply: "done"}, nil
			}
			provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: decisionServer.URL + "/v1/systemone"})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "off" {
				p.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: 10 * time.Second, VoiceWork: true, TextWork: true})
			}
			defer p.Decisions.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			p.AdmitInput = func(ctx context.Context, text string, background bool) (ResponseAdmission, error) {
				if mode != "off" && mode != "background" && mode != "control" {
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("decision not started before admission")
					}
					if mode != "late" {
						deadline := time.Now().Add(time.Second)
						for p.Decisions.InFlight() {
							if time.Now().After(deadline) {
								t.Fatal("decision did not finish")
							}
							time.Sleep(time.Millisecond)
						}
					}
				}
				switch mode {
				case "host-closed":
					jobs.Close()
				case "cancelled":
					cancel()
				case "settings":
					changed := a.WorkerSettings()
					changed.Revision++
					if err := a.ApplyWorkerSettings(changed); err != nil {
						t.Fatal(err)
					}
				case "disable":
					p.Decisions.Close()
				case "history":
					b.LoadHistory([]brain.Turn{{Role: "user", Content: "Do not run anything."}})
				case "persona":
					p.Brain = &fakeBrain{chunks: []string{"New persona."}}
				case "conversation":
					if err := p.ClearConversation(); err != nil {
						t.Fatal(err)
					}
				case "rewritten":
					text = "Explain how to check Git."
				}
				return ResponseAdmission{Request: text}, nil
			}
			if mode == "control" {
				p.JobActive = func() bool { return true }
			}
			switch mode {
			case "continuation":
				err = p.runTextTurn(withContinuation(ctx, continuationRequest{admission: ResponseAdmission{Request: input}}), input, false)
			case "voice":
				_, err = p.RunTurn(ctx)
			case "background":
				err = p.runTextTurn(ctx, input, true)
			case "control":
				err = p.RunTurnTextMode(ctx, "any update")
			default:
				err = p.RunTurnTextMode(ctx, input)
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled turn error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if (mode == "host-closed" || mode == "cancelled") && modelCalls.Load() != 0 {
				t.Fatalf("rejected turn made %d speaking calls", modelCalls.Load())
			}
			accepted := mode == "text" || mode == "voice" || mode == "early"
			if accepted {
				deadline := time.Now().Add(time.Second)
				for workers.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if workers.Load() != 1 || modelCalls.Load() != 0 {
					t.Fatalf("workers=%d speaking calls=%d", workers.Load(), modelCalls.Load())
				}
				history := b.History()
				if len(history) != 2 || history[0].Content != input || history[0].Role != "user" || history[1].Role != "assistant" || history[1].Content == "" {
					t.Fatalf("routed history=%+v", history)
				}
			} else {
				if workers.Load() != 0 || len(jobs.Status()) != 0 {
					t.Fatal("ineligible decision dispatched work")
				}
				wantCalls := int32(2) // route(chat), then tools-free answer
				if mode == "background" || mode == "continuation" {
					wantCalls = 1
				}
				if mode != "persona" && mode != "control" && mode != "cancelled" && mode != "host-closed" && modelCalls.Load() != wantCalls {
					t.Fatalf("fallback calls=%d", modelCalls.Load())
				}
			}
			if (mode == "off" || mode == "background" || mode == "control" || mode == "continuation") && decisionCalls.Load() != 0 {
				t.Fatalf("bypass calls=%d", decisionCalls.Load())
			}
		})
	}
}

func TestPendingWorkDecisionDoesNotDispatchOnLocalReply(t *testing.T) {
	var workers atomic.Int32
	entered := make(chan struct{})
	decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		one := 1.0
		_ = json.NewEncoder(w).Encode(decision.Response{
			Answers: map[string]decision.Answer{
				"work": {Noul: &one},
				"tier": {Choice: "simple", Confidence: &one, Probabilities: map[string]float64{"none": 0, "simple": 1, "medium": 0, "complex": 0}},
			},
		})
	}))
	defer decisionServer.Close()

	cfg := tierTestConfig()
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	brainProvider := &continuityHistoryBrain{
		fakeBrain: &fakeBrain{},
		snapshot:  brain.ContinuitySnapshot{},
	}
	p := &Pipeline{Brain: brainProvider, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("local-reply-decision"); err != nil {
		t.Fatal(err)
	}
	a := NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		workers.Add(1)
		return &lifecycleWorker{reply: "done"}, nil
	}
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: decisionServer.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	p.Decisions.Configure(decision.Options{
		Policy:   decision.WorkPolicy{Evaluator: provider},
		Budget:   time.Second,
		TextWork: true,
	})
	defer p.Decisions.Close()
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("decision did not start before local reply admission")
		}
		return ResponseAdmission{
			Request:    "",
			LocalReply: "Santa here.",
			Envelope:   events.ResponseEnvelope{ResponseID: "ack-1", PersonaID: "santa", DisplayName: "Santa"},
		}, nil
	}

	if err := p.RunTurnTextMode(context.Background(), "please inspect the source tree"); err != nil {
		t.Fatal(err)
	}
	if workers.Load() != 0 || len(jobs.Status()) != 0 {
		t.Fatalf("local reply dispatched worker work: workers=%d jobs=%d", workers.Load(), len(jobs.Status()))
	}
	deadline := time.Now().Add(time.Second)
	for p.Decisions.InFlight() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.Decisions.InFlight() {
		t.Fatal("decision remained in-flight after local reply return")
	}
}

type failedInputReuseEvaluator struct {
	mu         sync.Mutex
	calls      int
	firstReady chan struct{}
}

func (e *failedInputReuseEvaluator) Evaluate(ctx context.Context, _ decision.Request) (decision.Response, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call == 1 {
		close(e.firstReady)
		one := 1.0
		return decision.Response{
			Answers: map[string]decision.Answer{
				"work": {Noul: &one},
				"tier": {
					Choice:        "simple",
					Confidence:    &one,
					Probabilities: map[string]float64{"none": 0, "simple": 1, "medium": 0, "complex": 0},
				},
			},
		}, nil
	}
	<-ctx.Done()
	return decision.Response{}, ctx.Err()
}

type countedDecisionBrain struct {
	mu    sync.Mutex
	calls int
}

func (b *countedDecisionBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- "fallback answer"
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (*countedDecisionBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "fallback answer", nil
}
func (*countedDecisionBrain) ClearHistory()            {}
func (*countedDecisionBrain) History() []brain.Turn    { return nil }
func (*countedDecisionBrain) LoadHistory([]brain.Turn) {}

func (b *countedDecisionBrain) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func TestFailedInputDecisionProposalIsNotReused(t *testing.T) {
	cfg := tierTestConfig()
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	brainProvider := &countedDecisionBrain{}
	p := &Pipeline{Brain: brainProvider, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("failed-decision-reuse"); err != nil {
		t.Fatal(err)
	}
	a := NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	var workers atomic.Int32
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		workers.Add(1)
		return &lifecycleWorker{reply: "done"}, nil
	}
	evaluator := &failedInputReuseEvaluator{firstReady: make(chan struct{})}
	p.Decisions.Configure(decision.Options{
		Policy:   decision.WorkPolicy{Evaluator: evaluator},
		Budget:   time.Second,
		TextWork: true,
	})
	defer p.Decisions.Close()

	turn := 0
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		turn++
		if turn == 1 {
			select {
			case <-evaluator.firstReady:
			case <-time.After(time.Second):
				t.Fatal("first decision did not complete before admission failure")
			}
			return ResponseAdmission{}, errors.New("admission failed")
		}
		return ResponseAdmission{Request: "manual retry"}, nil
	}

	if err := p.RunTurnTextMode(t.Context(), "first request"); err == nil {
		t.Fatal("first turn should fail admission")
	}
	if workers.Load() != 0 {
		t.Fatalf("failed turn dispatched worker job: workers=%d", workers.Load())
	}

	if err := p.RunTurnTextMode(t.Context(), "manual retry"); err != nil {
		t.Fatalf("retry turn failed: %v", err)
	}
	if workers.Load() != 0 {
		t.Fatalf("stale decision proposal was reused: workers=%d", workers.Load())
	}
	if calls := brainProvider.Calls(); calls != 1 {
		t.Fatalf("brain fallback calls=%d want 1", calls)
	}
}
