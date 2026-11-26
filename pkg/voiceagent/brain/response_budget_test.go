package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

func TestRunawayResponseStopsAndReleasesTransport(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		t.Run(map[bool]string{false: "content", true: "thinking"}[thinking], func(t *testing.T) {
			stopped := make(chan struct{})
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Options["num_predict"] != float64(32) {
					t.Errorf("request limit = %v", req.Options["num_predict"])
				}
				// Deliberately ignore num_predict and never send Done. The
				// client fuse must still end this actively producing response.
				for {
					message := api.Message{Role: "assistant", Content: "The next step is to inspect the path. "}
					if thinking {
						message.Thinking, message.Content = message.Content, ""
					}
					if err := json.NewEncoder(w).Encode(api.ChatResponse{Message: message}); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-time.After(time.Millisecond):
					}
				}
			})
			o.cfg.OllamaResponseTokens = 32
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			stream, err := o.ThinkStream(ctx, "What next?", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			text, _ := drainStream(t, stream)
			result := <-stream.Done
			if !errors.Is(result.Err, ErrResponseLimit) || !result.Recovered || ctx.Err() != nil {
				t.Fatalf("runaway not recovered by output limit: %+v", result)
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("provider transport remained active")
			}
			history := o.History()
			if thinking {
				if text != "" || len(history) != 3 {
					t.Fatal("thinking became assistant history or speech")
				}
			} else if last := history[len(history)-1]; last.Content != strings.TrimSpace(text) || last.Disposition != "output_limit" {
				t.Fatal("partial transcript was lost or recorded as a complete reply")
			}
		})
	}
}

func TestResponseBudgetSpansRoutingAndDoesNotContinue(t *testing.T) {
	calls := 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if calls == 1 {
			if req.Options["num_predict"] != float64(32) {
				t.Errorf("initial cap = %v", req.Options["num_predict"])
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 20}, Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "chat"})}}}}})
			return
		}
		if calls != 2 || req.Options["num_predict"] != float64(44) {
			t.Errorf("response allowance restarted: call=%d cap=%v", calls, req.Options["num_predict"])
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "The path needs inspection. "}})
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "length", Metrics: api.Metrics{EvalCount: 44}})
	})
	o.cfg.OllamaResponseTokens = 64
	o.cfg.ContinuityIntegrityEnabled = true
	o.cfg.ContinuityRecoveryMax = 2
	stream, err := o.ThinkStream(t.Context(), "What should we inspect?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	drainStream(t, stream)
	if result := <-stream.Done; !errors.Is(result.Err, ErrResponseLimit) || !result.Recovered || calls != 2 {
		t.Fatalf("limit did not terminate turn: calls=%d result=%+v", calls, result)
	}
}

func TestIncompleteProjectionPreservesCanonicalEvidence(t *testing.T) {
	o := repetitionBrain(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected inference") })
	raw := strings.Repeat("A partial response that never finished. ", 400)
	turn := Turn{Role: "assistant", Content: raw, PersonaID: "other", PersonaDisplayName: "Other", ResponseID: "failed-response", Disposition: "interrupted"}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "Inspect the garden."}, turn}); err != nil {
		t.Fatal(err)
	}
	if got := o.History()[1]; got.Content != raw || got.Disposition != "interrupted" || got.ResponseID != turn.ResponseID {
		t.Fatal("canonical evidence was rewritten")
	}
	for _, content := range []string{o.buildMessages("")[2].Content, cliHistoryRecord(turn, nil)} {
		if len(content) > 1800 || !strings.Contains(content, "Incomplete assistant response") || !strings.Contains(content, "Other") {
			t.Fatal("foreign partial response was unbounded or lost attribution")
		}
	}
	turn.Disposition = ""
	if modelTurnContent(turn) != raw {
		t.Fatal("complete long response was shortened")
	}
}

func TestHostNoteReachesTheModelButNotTheReply(t *testing.T) {
	turn := Turn{Role: "assistant", Content: "Stopped after 40 seconds.", HostNote: "[job-1 was cancelled before it finished.]"}
	if got, want := modelTurnContent(turn), "Stopped after 40 seconds. [job-1 was cancelled before it finished.]"; got != want {
		t.Fatalf("model content = %q, want %q", got, want)
	}
	turn.Disposition = "interrupted"
	got := modelTurnContent(turn)
	if !strings.HasPrefix(got, "[Incomplete assistant response") || !strings.HasSuffix(got, turn.HostNote) {
		t.Fatalf("model content of an unfinished reply = %q, want the marker first and the note last", got)
	}
	if user := (Turn{Role: "user", Content: "cancel", HostNote: "ignored"}); modelTurnContent(user) != "cancel" {
		t.Fatal("a host note changed a user turn")
	}
}

func TestFullResponseLimitRollsBackAndWorkerBudgetRemainsSeparate(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "worker"}[worker], func(t *testing.T) {
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if worker {
					if _, exists := req.Options["num_predict"]; exists {
						t.Error("foreground cap applied to legacy worker")
					}
				} else if req.Options["num_predict"] != float64(32) {
					t.Errorf("cap=%v", req.Options["num_predict"])
				}
				reason := "length"
				if worker {
					reason = "stop"
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: reason, Metrics: api.Metrics{EvalCount: 32}, Message: api.Message{Role: "assistant", Content: "A valid worker report about the garden."}})
			})
			o.cfg.OllamaResponseTokens = 32
			opts := StreamOptions{}
			if worker {
				opts = WorkerStreamOptions(false)
			}
			answer, err := o.ThinkFull(t.Context(), "Inspect the garden.", opts)
			if worker {
				if err != nil || answer == "" {
					t.Fatalf("worker answer=%q err=%v", answer, err)
				}
			} else if !errors.Is(err, ErrResponseLimit) || answer != "" || len(o.History()) != 2 {
				t.Fatalf("incomplete full reply leaked: answer=%q err=%v turns=%d", answer, err, len(o.History()))
			}
		})
	}
}

func TestResponseBudgetChargesMalformedToolRepair(t *testing.T) {
	calls := 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if calls <= 2 {
			if req.Options["num_predict"] != float64(32) {
				t.Errorf("repair allowance=%v", req.Options["num_predict"])
			}
		} else {
			t.Errorf("unbudgeted extra generation: %d", calls)
		}
		if calls == 1 {
			http.Error(w, `{"error":"XML syntax error: unexpected EOF in function parameter"}`, 500)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "length", Metrics: api.Metrics{EvalCount: 32}, Message: api.Message{Role: "assistant", Content: "An incomplete repair"}})
	})
	o.cfg.OllamaResponseTokens = 64
	stream, err := o.ThinkStream(t.Context(), "Inspect the garden.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := drainStream(t, stream)
	result := <-stream.Done
	if !errors.Is(result.Err, ErrResponseLimit) || calls != 2 || text != "" {
		t.Fatalf("repair restarted allowance: calls=%d text=%q result=%+v", calls, text, result)
	}
}

// Each tool round starts with the full allowance, and the byte fuse counts one
// request: two rounds that together pass the fuse are not a runaway.
func TestResponseBudgetRefillsAfterEachToolRound(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		cfg := liveDefaults(t)
		cfg.OllamaResponseTokens = 64
		l := newLiveTurns(t, cfg, "")
		reasoning := strings.Repeat("checking the next file ", 30)
		reply := func(n int, req api.ChatRequest) api.ChatResponse {
			if n <= 2 {
				r := toolResponse(readFileCall(fmt.Sprintf("part-%d.txt", n)))
				r.Message.Thinking, r.Metrics.EvalCount = reasoning, 30
				return r
			}
			r := answerResponse("Both parts are missing.")
			r.Metrics.EvalCount = 10
			return r
		}
		answer, err, reqs := l.run(t.Context(), streaming, "Read both parts.", reply)
		if err != nil || !strings.Contains(answer, "missing") || len(reqs) != 3 {
			t.Fatalf("answer=%q err=%v requests=%d", answer, err, len(reqs))
		}
		for i, req := range reqs {
			if req.Options["num_predict"] != float64(32) {
				t.Fatalf("request %d cap = %v, want the full allowance's tool-round share 32", i+1, req.Options["num_predict"])
			}
		}
	})
}
