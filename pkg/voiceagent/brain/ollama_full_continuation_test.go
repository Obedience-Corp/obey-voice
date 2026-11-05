package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/ollama/ollama/api"
)

func fullReply(content, reason string) api.ChatResponse {
	return api.ChatResponse{Done: true, DoneReason: reason, Metrics: api.Metrics{EvalCount: 4}, Message: api.Message{Role: "assistant", Content: content}}
}

func TestOllamaFullWorkerContinuesWithoutRepeatingCompletedTools(t *testing.T) {
	dir := t.TempDir()
	calls, tools, usage := 0, 0, 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Options["num_predict"] != float64(100-(calls-1)*4) {
			t.Errorf("job output allowance reset: %v", req.Options)
		}
		response := fullReply("", "stop")
		switch calls {
		case 1:
			response.Message.ToolCalls = []api.ToolCall{{ID: "write-report", Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "report.txt", "content": "ready"})}}}
		case 2:
			response = fullReply("The report is ", "length")
		case 3:
			if len(req.Tools) == 0 {
				t.Error("worker lost its tools during continuation")
			}
			foundResult, foundPartial := false, false
			for _, m := range req.Messages {
				foundResult = foundResult || m.Role == "tool" && m.ToolCallID == "write-report"
				foundPartial = foundPartial || m.Role == "assistant" && m.Content == "The report is "
			}
			if !foundResult || !foundPartial {
				t.Errorf("continuation lost tool evidence or partial answer: %+v", req.Messages)
			}
			response = fullReply("ready.", "stop")
		default:
			t.Error("unexpected extra request")
			http.Error(w, "extra", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	o.ClearHistory()
	o.workDir = dir
	opts := WorkerStreamOptions(true)
	opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 3, MaxOutputTokens: 100})
	opts.OnToolEnd = func(ToolResult) { tools++ }
	opts.OnWorkerUsage = func(WorkerUsage) { usage++ }
	answer, err := o.ThinkFull(t.Context(), "Write report.txt and report its status.", opts)
	if err != nil || answer != "The report is ready." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if calls != 3 || tools != 1 || usage != 3 {
		t.Fatalf("requests=%d tools=%d usage=%d", calls, tools, usage)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "report.txt")); err != nil || string(data) != "ready" {
		t.Fatalf("completed write lost: %q %v", data, err)
	}
	history := o.History()
	if len(history) != 4 || history[0].Role != "user" || history[3].Content != answer {
		t.Fatalf("partial answer or host continuation leaked into history: %+v", history)
	}
	if snapshot := opts.WorkerBudget.Snapshot(); snapshot.Requests != 3 || snapshot.OutputTokens != 12 || snapshot.ToolCalls != 1 {
		t.Fatalf("continuation bypassed accounting: %+v", snapshot)
	}
}

func TestOllamaFullWorkerDoesNotExecuteTruncatedToolCalls(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed", true: "raw markup"}[raw], func(t *testing.T) {
			dir := t.TempDir()
			calls, tools := 0, 0
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				response := fullReply("", "stop")
				switch calls {
				case 1:
					response.DoneReason = "length"
					if raw {
						response.Message.Content = "<tool_call><function=write_file><parameter=path>incomplete.txt"
					} else {
						response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "incomplete.txt", "content": "partial"})}}}
					}
				case 2:
					response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "complete.txt", "content": "ready"})}}}
				case 3:
					response.Message.Content = "The report is ready."
				default:
					t.Error("unexpected extra request")
					http.Error(w, "extra", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			o.workDir = dir
			opts := WorkerStreamOptions(true)
			opts.OnToolEnd = func(ToolResult) { tools++ }
			answer, err := o.ThinkFull(t.Context(), "Write a status report.", opts)
			if err != nil || answer != "The report is ready." || calls != 3 || tools != 1 {
				t.Fatalf("answer=%q err=%v requests=%d tools=%d", answer, err, calls, tools)
			}
			if _, err := os.Stat(filepath.Join(dir, "incomplete.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("truncated tool call executed: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(dir, "complete.txt")); err != nil || string(data) != "ready" {
				t.Fatalf("complete tool call did not run: %q %v", data, err)
			}
		})
	}
}

func TestOllamaFullWorkerContinuationHonorsLimits(t *testing.T) {
	for _, mode := range []string{"requests", "tokens", "unknown usage", "cancel", "context", "exhaustion"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				response := fullReply("An incomplete report ", "length")
				if mode == "unknown usage" {
					response.Metrics.EvalCount = 0
				}
				if mode == "context" {
					response.Message.Content = strings.Repeat("word ", 8000)
				}
				if mode == "cancel" {
					cancel()
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			opts := WorkerStreamOptions(false)
			wantErr := ErrWorkerBudgetExceeded
			wantCalls := 1
			switch mode {
			case "requests":
				opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 1})
			case "tokens":
				opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxOutputTokens: 4})
			case "unknown usage":
				opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxOutputTokens: 100})
			case "cancel":
				wantErr = context.Canceled
			case "context":
				wantErr = ErrPromptBudget
			case "exhaustion":
				wantCalls = 3
				wantErr = nil
			}
			answer, err := o.ThinkFull(ctx, "Inspect the garden.", opts)
			if err == nil || answer != "" || calls != wantCalls || wantErr != nil && !errors.Is(err, wantErr) {
				t.Fatalf("answer=%q err=%v calls=%d want=%d", answer, err, calls, wantCalls)
			}
			if mode == "exhaustion" && !strings.Contains(err.Error(), "continuation attempts exhausted") {
				t.Fatalf("unhelpful exhaustion error: %v", err)
			}
			if len(o.History()) != 2 {
				t.Fatal("failed continuation recorded a complete answer")
			}
		})
	}
}

func TestOllamaFullWorkerContinuationAllowanceSpansToolRounds(t *testing.T) {
	calls, tools := 0, 0
	o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		response := fullReply("I will inspect ", "length")
		if calls%2 == 0 {
			response = fullReply("", "stop")
			response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "list_files", Arguments: mustArgs(map[string]any{"path": "."})}}}
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	o.workDir = t.TempDir()
	opts := WorkerStreamOptions(true)
	opts.OnToolEnd = func(ToolResult) { tools++ }
	answer, err := o.ThinkFull(t.Context(), "Inspect the project.", opts)
	if err == nil || answer != "" || calls != 5 || tools != 2 || !strings.Contains(err.Error(), "continuation attempts exhausted") {
		t.Fatalf("allowance restarted across tools: answer=%q err=%v calls=%d tools=%d", answer, err, calls, tools)
	}
}

func TestOllamaFullWorkerContinuesAfterReasoningCutoff(t *testing.T) {
	for _, tagged := range []bool{false, true} {
		t.Run(map[bool]string{false: "thinking field", true: "think tags"}[tagged], func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				response := fullReply("The report is ready.", "stop")
				if calls == 1 {
					response = fullReply("", "length")
					if tagged {
						response.Message.Content = "<think>Private reasoning cut off"
					} else {
						response.Message.Thinking = "Private reasoning cut off"
					}
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			answer, err := o.ThinkFull(t.Context(), "Summarize the report.", WorkerStreamOptions(false))
			if err != nil || answer != "The report is ready." || calls != 2 {
				t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls)
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, "Private reasoning") {
					t.Fatal("reasoning leaked into canonical history")
				}
			}
		})
	}
}

func TestOllamaFullContinuationDoesNotRestartOtherRequests(t *testing.T) {
	for _, mode := range []string{"foreground", "summary", "notification", "handoff", "discussion"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_ = json.NewEncoder(w).Encode(fullReply("An incomplete report ", "length"))
			})
			opts := WorkerStreamOptions(false)
			switch mode {
			case "foreground":
				opts = StreamOptions{}
			case "summary":
				opts.TransientHistory = true
			case "notification":
				opts.Background = true
			case "handoff":
				opts.AgentContinuation = true
			case "discussion":
				opts.Discussion = &discussion.Directive{}
			}
			answer, err := o.ThinkFull(t.Context(), "Inspect the garden.", opts)
			if err == nil || answer != "" || calls != 1 {
				t.Fatalf("unexpected continuation: answer=%q err=%v calls=%d", answer, err, calls)
			}
		})
	}
}

func TestOllamaFullWorkerContinuationRechecksAdmission(t *testing.T) {
	calls, admissions := 0, 0
	o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(fullReply("The report is ", "length"))
	})
	denied := errors.New("workers disabled by host")
	budget := NewWorkerBudget(BudgetLimits{MaxRequests: 3})
	budget.BeforeRequest = func() error {
		admissions++
		if admissions > 1 {
			return denied
		}
		return nil
	}
	opts := WorkerStreamOptions(false)
	opts.WorkerBudget = budget
	answer, err := o.ThinkFull(t.Context(), "Inspect the report.", opts)
	if !errors.Is(err, denied) || answer != "" || calls != 1 || admissions != 2 {
		t.Fatalf("continued after policy revocation: answer=%q err=%v calls=%d admissions=%d", answer, err, calls, admissions)
	}
}

func TestOllamaFullWorkerContinuationRejectsIncompleteFinish(t *testing.T) {
	for _, mode := range []string{"empty", "empty after prose", "missing done", "provider error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				response := fullReply("", "length")
				response.Message.Thinking = "Private reasoning"
				if mode == "empty after prose" {
					response.Message.Content = "An incomplete report "
				}
				if calls == 2 {
					if mode == "provider error" {
						http.Error(w, "unavailable", 500)
						return
					}
					response = fullReply("", "stop")
					if mode == "missing done" {
						response.Message.Content = "A report that never finished"
						response.Done = false
					}
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			answer, err := o.ThinkFull(t.Context(), "Inspect the report.", WorkerStreamOptions(false))
			if err == nil || answer != "" || calls != 2 {
				t.Fatalf("incomplete continuation succeeded or retried: answer=%q err=%v calls=%d", answer, err, calls)
			}
		})
	}
}

func TestOllamaFullWorkerContinuationCanFinishMidToken(t *testing.T) {
	for _, parts := range [][2]string{{"The report is read", "y."}, {"The report is ready", "."}} {
		t.Run(parts[1], func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				response := fullReply(parts[0], "length")
				if calls == 2 {
					response = fullReply(parts[1], "stop")
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			answer, err := o.ThinkFull(t.Context(), "Inspect the report.", WorkerStreamOptions(false))
			if err != nil || answer != parts[0]+parts[1] || calls != 2 {
				t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls)
			}
		})
	}
}
