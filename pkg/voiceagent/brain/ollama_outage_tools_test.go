package brain

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ollama/ollama/api"
)

func TestOllamaOutageAfterToolDoesNotRepeatEffects(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "recovered"}[recover], func(t *testing.T) {
			var calls atomic.Int32
			o := outageTestBrain(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "result.txt", "content": "cobalt"})}}}}})
					return
				}
				if n == 3 && recover {
					var req api.ChatRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					results := 0
					for _, m := range req.Messages {
						if m.Role == "tool" {
							results++
						}
					}
					if results != 1 {
						t.Errorf("tool results=%d", results)
					}
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Role: "assistant", Content: "Saved cobalt."}})
					return
				}
				http.Error(w, workerExitFixture, 500)
			})
			executions := 0
			opts := WorkerStreamOptions(true)
			opts.OnToolStart = func(ToolCall) { executions++ }
			stream, err := o.ThinkStream(t.Context(), "Save cobalt to result.txt", opts)
			if err != nil {
				t.Fatal(err)
			}
			answer, _ := drainStream(t, stream)
			result := <-stream.Done
			if executions != 1 || calls.Load() != 3 {
				t.Fatalf("effects=%d requests=%d error=%v", executions, calls.Load(), result.Err)
			}
			data, err := os.ReadFile(filepath.Join(o.workDir, "result.txt"))
			if err != nil || !strings.Contains(string(data), "cobalt") {
				t.Fatalf("tool output=%q err=%v", data, err)
			}
			if (result.Err == nil) != recover || recover && answer != "Saved cobalt." {
				t.Fatalf("answer=%q result=%+v", answer, result)
			}
			if _, safe := o.FailedInputRetry(); safe {
				t.Fatal("tool-using request exposed manual replay")
			}
		})
	}
}
