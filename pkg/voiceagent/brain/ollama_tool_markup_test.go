package brain

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

const rawToolTextFixture = "<function=read_file>\n<parameter=path>\n/workspace/campaigns/example\n</parameter>\n</function>\n</tool_call>"

func TestOllamaRejectsRawToolMarkupWithoutExecutionOrReply(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, fixture := range []string{`<atem:function_calls>
<atem:invoke name="route">
<atem:parameter name="kind">spawn</atem:parameter>
<atem:parameter name="source">user</atem:parameter>
</atem:invoke>
</atem:function_calls>`, rawToolTextFixture, "<tool_call>", "<tool_call", "</function", "<function=read_file"} {
			t.Run(map[bool]string{false: "full", true: "stream"}[streaming]+"/"+fixture[:min(18, len(fixture))], func(t *testing.T) {
				var requests, executions atomic.Int32
				o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if streaming {
						for _, b := range []byte(fixture) {
							_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: string(b)}})
						}
					} else {
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: fixture}, Done: true, DoneReason: "stop"})
					}
					if streaming {
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
					}
				})
				opts := WorkerStreamOptions(true)
				opts.OnToolStart = func(ToolCall) { executions.Add(1) }
				var reply string
				var err error
				if streaming {
					stream, startErr := o.ThinkStream(t.Context(), "Inspect the example campaign.", opts)
					if startErr != nil {
						t.Fatal(startErr)
					}
					reply, _ = drainStream(t, stream)
					result := <-stream.Done
					err = result.Err
					if !result.Recovered {
						t.Fatalf("malformed output was not reported as recoverable: %+v", result)
					}
				} else {
					reply, err = o.ThinkFull(t.Context(), "Inspect the example campaign.", opts)
				}
				if !errors.Is(err, textclean.ErrRawToolMarkup) || reply != "" {
					t.Fatalf("reply=%q err=%v", reply, err)
				}
				if executions.Load() != 0 || requests.Load() != 1 {
					t.Fatalf("inferred tool or unbounded repair: executions=%d requests=%d", executions.Load(), requests.Load())
				}
				for name, history := range map[string][]Turn{"history": o.History(), "canonical": o.CanonicalHistory(), "context": o.ContextHistory()} {
					for _, turn := range history {
						if turn.Role == "assistant" && (strings.Contains(turn.Content, "<") || len(turn.ToolCalls) > 0) {
							t.Fatalf("raw invocation retained in %s: %+v", name, turn)
						}
					}
				}
			})
		}
	}
}

func TestOllamaRawMarkupKeepsOnlyGenuineStreamedPartialReply(t *testing.T) {
	prefix := "I can inspect the example. "
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: prefix}})
		for _, b := range []byte(rawToolTextFixture) {
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: string(b)}})
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
	})
	stream, err := o.ThinkStream(t.Context(), "Inspect the example campaign.", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := drainStream(t, stream)
	result := <-stream.Done
	if strings.TrimSpace(reply) != strings.TrimSpace(prefix) || !errors.Is(result.Err, textclean.ErrRawToolMarkup) || !result.Recovered {
		t.Fatalf("reply=%q result=%+v", reply, result)
	}
	history := o.History()
	last := history[len(history)-1]
	if last.Role != "assistant" || last.Content != strings.TrimSpace(prefix) {
		t.Fatalf("partial reply not preserved safely: %+v", last)
	}
}

func TestOllamaToolMarkupValidationPreservesComparisonsAndCodeExamples(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, reply := range []string{"Three < five and eight > six.", "The literal `<function=read_file>` is an example.", "```xml\n" + rawToolTextFixture + "\n```\nThat was a code example."} {
			t.Run(map[bool]string{false: "full", true: "stream"}[streaming]+"/"+reply[:min(12, len(reply))], func(t *testing.T) {
				o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
					if streaming {
						for _, b := range []byte(reply) {
							_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: string(b)}})
						}
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
					} else {
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: reply}})
					}
				})
				var got string
				var err error
				if streaming {
					stream, startErr := o.ThinkStream(t.Context(), "Explain the example.", StreamOptions{})
					if startErr != nil {
						t.Fatal(startErr)
					}
					got, _ = drainStream(t, stream)
					err = (<-stream.Done).Err
				} else {
					got, err = o.ThinkFull(t.Context(), "Explain the example.", StreamOptions{})
				}
				if err != nil || strings.TrimSpace(got) == "" {
					t.Fatalf("ordinary explanation rejected: got=%q err=%v", got, err)
				}
			})
		}
	}
}

func TestCleanForVoiceDefensivelyDropsRawToolPayload(t *testing.T) {
	if got := CleanForVoice(rawToolTextFixture); got != "" {
		t.Fatalf("raw tool payload survived final cleaner: %q", got)
	}
	if got := CleanForVoice("I can check that. " + rawToolTextFixture); got != "I can check that." {
		t.Fatalf("genuine prefix changed: %q", got)
	}
	if got := CleanForVoice("Three < five."); got != "Three < five." {
		t.Fatalf("comparison changed: %q", got)
	}
}
