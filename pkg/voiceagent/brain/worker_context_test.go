package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestWorkerContextUsesSelectedModelCapacity(t *testing.T) {
	for _, capacity := range []int{0, 65536, 262144} {
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req api.ShowRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Model != "worker-model" {
					t.Errorf("metadata model=%q", req.Model)
				}
				info := map[string]any{"general.architecture": "fixture"}
				if capacity > 0 {
					info["fixture.context_length"] = capacity
				}
				_ = json.NewEncoder(w).Encode(api.ShowResponse{ModelInfo: info})
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			cfg := &config.Config{OllamaModel: "worker-model", IsWorker: true}
			got, err := resolveWorkerContext(t.Context(), api.NewClient(host, server.Client()), cfg)
			want := capacity
			if want == 0 {
				want = fallbackWorkerContext
			}
			if err != nil || got.OllamaNumCtx != want || got.ContinuityNumCtxCeil != want {
				t.Fatalf("resolved=%+v err=%v", got, err)
			}
			if cfg.OllamaNumCtx != 0 {
				t.Fatal("resolved window mutated the admission config")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := resolveWorkerContext(ctx, api.NewClient(host, server.Client()), cfg); !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestWorkerConstructorSendsResolvedContextAndPreservesOverrides(t *testing.T) {
	for _, window := range []int{0, 8192, 262144} {
		t.Run(strconv.Itoa(window), func(t *testing.T) {
			shows, chats := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "chat-model"}}})
				case "/api/show":
					shows++
					_ = json.NewEncoder(w).Encode(api.ShowResponse{ModelInfo: map[string]any{"general.architecture": "fixture", "fixture.context_length": 65536}})
				case "/api/chat":
					chats++
					var req api.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					want := window
					if want == 0 {
						want = 65536
					}
					if req.Options["num_ctx"] != float64(want) {
						t.Errorf("sent context=%v want=%d", req.Options["num_ctx"], want)
					}
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: "The artifact is ready."}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := ollamaConstructorConfig(t, server.URL)
			cfg.SkillsEnabled, cfg.OllamaEmbeddingModel = false, ""
			cfg.IsWorker, cfg.OllamaNumCtx = true, window
			o, err := newOllamaHTTP(t.Context(), cfg, server.Client(), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			worker := o.Fresh().(*OllamaBrain)
			if _, err := worker.ThinkFull(t.Context(), "Inspect the artifact.", WorkerStreamOptions(false)); err != nil {
				t.Fatal(err)
			}
			wantShows := 0
			if window == 0 {
				wantShows = 1
			}
			if chats != 1 || shows != wantShows || cfg.OllamaNumCtx != window {
				t.Fatalf("chats=%d metadata requests=%d original context=%d", chats, shows, cfg.OllamaNumCtx)
			}
		})
	}
}

func TestWorkerCompactsDuringToolLoopWithoutRepeatingWork(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "full"}[streaming], func(t *testing.T) {
			dir := t.TempDir()
			payload := "Important finding: artifact A is ready. " + strings.Repeat("tool detail ", 550)
			for i := 1; i <= 4; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("artifact-%d.txt", i)), []byte(payload), 0600); err != nil {
					t.Fatal(err)
				}
			}
			requests, summaries, toolsRun := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				response := api.ChatResponse{Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 32}, Message: api.Message{Role: "assistant"}}
				if len(req.Tools) == 0 {
					summaries++
					if req.Stream == nil || *req.Stream || !strings.Contains(req.Messages[len(req.Messages)-1].Content, "Summarize this worker task") {
						t.Error("compaction must be a private tools-free request")
					}
					response.Message.Content = "Original task: inspect the artifact. Completed reads confirmed artifact A is ready; continue from the latest result without repeating commands."
				} else {
					requests++
					if requests <= 4 {
						response.Message.ToolCalls = []api.ToolCall{{ID: "call", Function: api.ToolCallFunction{Name: "read_file", Arguments: mustArgs(map[string]any{"path": fmt.Sprintf("artifact-%d.txt", requests)})}}}
					} else {
						if summaries == 0 {
							t.Error("worker reached final answer without compacting")
						}
						if req.Messages[1].Role != "user" || req.Messages[1].Content != "Inspect the artifact." {
							t.Error("compaction lost the original task")
						}
						response.Message.Content = "Artifact A is ready."
					}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			cfg := continuityConfig()
			cfg.IsWorker, cfg.ContinuityProjectionEnabled = true, false
			cfg.ContinuitySoftLimitRatio = 0.5
			o := &OllamaBrain{cfg: cfg, client: api.NewClient(host, server.Client()), model: "fixture", workDir: dir}
			opts := WorkerStreamOptions(true)
			opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 10, MaxToolCalls: 4, MaxOutputTokens: 320})
			opts.OnToolEnd = func(ToolResult) { toolsRun++ }
			var answer string
			var err error
			if streaming {
				stream, streamErr := o.ThinkStream(t.Context(), "Inspect the artifact.", opts)
				err = streamErr
				if err == nil {
					answer, _ = drainStream(t, stream)
					err = (<-stream.Done).Err
				}
			} else {
				answer, err = o.ThinkFull(t.Context(), "Inspect the artifact.", opts)
			}
			if err != nil || !strings.Contains(answer, "Artifact A is ready") || toolsRun != 4 || summaries == 0 {
				t.Fatalf("answer=%q err=%v tools=%d summaries=%d", answer, err, toolsRun, summaries)
			}
			if usage := opts.WorkerBudget.Snapshot(); usage.Requests != requests+summaries || usage.OutputTokens != (requests+summaries)*32 || usage.ToolCalls != 4 {
				t.Fatalf("summary inference escaped job accounting: %+v", usage)
			}
			raw := 0
			for _, turn := range o.CanonicalHistory() {
				if turn.Role == "tool" && turn.Content == payload {
					raw++
				}
			}
			if raw != toolsRun {
				t.Fatalf("compaction erased canonical evidence: %d results for %d tools", raw, toolsRun)
			}
			for i, m := range o.history {
				if m.Role == "tool" && (i == 0 || len(o.history[i-1].ToolCalls) == 0) {
					t.Fatal("compaction orphaned a tool result")
				}
			}
		})
	}
}

func TestWorkerCompactionFailurePreservesContextAndHonorsBudget(t *testing.T) {
	for _, mode := range []string{"empty", "truncated", "incomplete", "error", "budget", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "error" {
					http.Error(w, "unavailable", 500)
					return
				}
				response := api.ChatResponse{Done: mode != "incomplete", DoneReason: "stop", Metrics: api.Metrics{EvalCount: 12}, Message: api.Message{Content: "Task notes."}}
				if mode == "empty" {
					response.Message.Content = ""
				}
				if mode == "truncated" {
					response.DoneReason = "length"
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			cfg := continuityConfig()
			cfg.IsWorker, cfg.ContinuitySoftLimitRatio = true, 0.3
			o := &OllamaBrain{cfg: cfg, model: "fixture", client: api.NewClient(host, server.Client())}
			o.appendMessage(api.Message{Role: "user", Content: "Inspect the artifact."}, nil)
			o.appendMessage(api.Message{Role: "assistant", Content: strings.Repeat("Recorded finding. ", 700)}, nil)
			before, _ := json.Marshal(o.history)
			canonical, _ := json.Marshal(o.CanonicalHistory())
			budget := NewWorkerBudget(BudgetLimits{MaxRequests: 1})
			if mode == "budget" {
				if err := budget.AcquireRequest(t.Context()); err != nil {
					t.Fatal(err)
				}
				budget.RecordUsage(1, 1)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			opts := WorkerStreamOptions(true)
			opts.WorkerBudget = budget
			err := o.prepareWorkerContext(budget.Context(ctx), "", nil, opts)
			if err == nil {
				t.Fatal("failed summary accepted")
			}
			if mode == "budget" && (!errors.Is(err, ErrWorkerBudgetExceeded) || calls != 0) {
				t.Fatalf("compaction bypassed worker budget: calls=%d err=%v", calls, err)
			}
			if mode == "cancel" && (!errors.Is(err, context.Canceled) || calls != 0) {
				t.Fatalf("compaction bypassed cancellation: calls=%d err=%v", calls, err)
			}
			after, _ := json.Marshal(o.history)
			afterCanonical, _ := json.Marshal(o.CanonicalHistory())
			if string(before) != string(after) || string(canonical) != string(afterCanonical) {
				t.Fatal("failed compaction replaced task evidence")
			}
		})
	}
}
