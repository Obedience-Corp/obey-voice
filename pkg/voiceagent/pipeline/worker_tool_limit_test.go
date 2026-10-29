package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

func TestOllamaWorkerLimitDeliversAnswerOrFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "answer", true: "failure"}[fail], func(t *testing.T) {
			const reply = "The file is missing. Here is the draft from our conversation."
			rounds := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
					return
				}
				if r.URL.Path == "/api/show" {
					_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`))
					return
				}
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				response := api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant"}}
				if len(req.Tools) > 0 {
					rounds++
					if rounds > 20 {
						t.Error("unbounded tools")
						http.Error(w, "loop", 500)
						return
					}
					args := api.NewToolCallFunctionArguments()
					args.Set("path", "missing.txt")
					response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: args}}}
				} else if !fail {
					response.Message.Content = reply
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: t.TempDir(), PromptsDir: t.TempDir(), MaxHistory: 100, OllamaNumCtx: 32768, VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true}
			worker, err := brain.NewOllama(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			p := &Pipeline{Events: events.NewBus()}
			p.SetOutputMuted(true)
			table := job.NewTable(t.Context())
			defer table.Close()
			a := NewAgency(p, table, worker, cfg, nil)
			a.Wire()
			var completed events.JobFinished
			var failed events.JobFailed
			events.Subscribe(p.Events, func(e events.JobFinished) { completed = e })
			events.Subscribe(p.Events, func(e events.JobFailed) { failed = e })
			done := make(chan struct{})
			table.OnDone(func(id string, state job.State, result string, err error) {
				a.onJobDone(id, state, result, err)
				close(done)
			})
			metrics := newTurnMetrics()
			a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Read missing.txt and draft a persona"})
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not finish")
			}
			if rounds == 0 {
				t.Fatal("fixture did not exercise tools")
			}
			items := p.Workers()
			if len(items) != 1 || len(items[0].Tools) != rounds {
				t.Fatalf("tool evidence lost: %+v", items)
			}
			if fail {
				if failed.ID == "" || failed.Err == "" || completed.ID != "" || items[0].State != "failed" {
					t.Fatalf("failure reported as completion: failed=%+v finished=%+v worker=%+v", failed, completed, items[0])
				}
			} else if completed.Summary != reply || failed.ID != "" || items[0].Result != reply {
				t.Fatalf("model answer not delivered: failed=%+v finished=%+v worker=%+v", failed, completed, items[0])
			}
		})
	}
}
