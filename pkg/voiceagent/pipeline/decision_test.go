package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	for _, mode := range []string{"text", "voice", "off", "late", "failure", "unavailable", "settings", "disable", "history", "persona", "conversation", "rewritten", "background", "control", "continuation", "host-closed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var modelCalls, decisionCalls, workers atomic.Int32
			liveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
					return
				}
				modelCalls.Add(1)
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
				if mode == "failure" {
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
			accepted := mode == "text" || mode == "voice"
			if accepted {
				deadline := time.Now().Add(time.Second)
				for workers.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if workers.Load() != 1 || modelCalls.Load() != 0 {
					t.Fatalf("workers=%d speaking calls=%d", workers.Load(), modelCalls.Load())
				}
				history := b.History()
				if len(history) != 2 || history[0].Content != input || history[1].Role != "assistant" || history[1].Content == "" {
					t.Fatalf("routed history=%+v", history)
				}
			} else {
				if workers.Load() != 0 || len(jobs.Status()) != 0 {
					t.Fatal("ineligible decision dispatched work")
				}
				if mode != "persona" && mode != "control" && mode != "cancelled" && mode != "host-closed" && modelCalls.Load() != 1 {
					t.Fatalf("fallback calls=%d", modelCalls.Load())
				}
			}
			if (mode == "off" || mode == "background" || mode == "control" || mode == "continuation") && decisionCalls.Load() != 0 {
				t.Fatalf("bypass calls=%d", decisionCalls.Load())
			}
		})
	}
}
