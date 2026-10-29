package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const gardenReply = "The garden has blue flowers, two apple trees and a pond. We can add a bench by the pond and lavender along the path. A wooden fence would shelter the beds from wind. Leave room near the gate for a wheelbarrow and keep the stepping stones level."

func repetitionBrain(t *testing.T, handler http.HandlerFunc) *OllamaBrain {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	host, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", cfg: &config.Config{ActivePersona: "veronica", AgentName: "Veronica", MaxHistory: 20, OllamaNumCtx: 8192}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "Describe the garden."}, {Role: "assistant", Content: gardenReply, PersonaID: "veronica", PersonaDisplayName: "Veronica", ResponseID: "old"}}); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRepeatedReplyRecovery(t *testing.T) {
	fresh := "The bench should cost about one hundred dollars."
	for _, tc := range []struct {
		name, first, decision, retry, want string
		calls                              int
		degraded                           bool
	}{
		{"fresh", fresh, "", "", fresh, 1, false},
		{"shared opening", gardenReply[:70] + " The bench can face west.", "", "", gardenReply[:70] + " The bench can face west.", 1, false},
		{"shorter answer", gardenReply[:100], "", "", gardenReply[:100], 1, false},
		{"whole replay", gardenReply, `{"decision":"unrequested"}`, fresh, fresh, 3, false},
		{"replay and new tail", gardenReply + " The bench can face west.", `{"decision":"unrequested"}`, fresh, fresh, 3, false},
		{"requested repeat", gardenReply, `{"decision":"requested"}`, "", gardenReply, 2, false},
		{"uncertain repeat", gardenReply, `{"decision":"uncertain"}`, "", gardenReply, 2, false},
		{"invalid classifier", gardenReply, `{}`, "", gardenReply, 2, false},
		{"unavailable classifier", gardenReply, "http error", "", gardenReply, 2, false},
		{"bounded retry", gardenReply, `{"decision":"unrequested"}`, gardenReply, "", 3, true},
		{"retry provider failure", gardenReply, `{"decision":"unrequested"}`, "http error", "", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []api.ChatRequest
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requests = append(requests, req)
				var reply string
				switch len(requests) {
				case 1:
					reply = tc.first
				case 2:
					reply = tc.decision
				case 3:
					reply = tc.retry
				default:
					t.Error("unbounded retry")
					http.Error(w, "extra", 500)
					return
				}
				if reply == "http error" {
					http.Error(w, "provider unavailable", 500)
					return
				}
				// Small deltas ensure no matching prefix leaks before the decision.
				for len(reply) > 0 {
					n := min(17, len(reply))
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: reply[:n]}})
					reply = reply[n:]
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
			})
			opts := StreamOptions{ToolsEnabled: true, Identity: &TurnIdentity{PersonaID: "veronica", ResponseID: "new"}}
			stream, err := o.ThinkStream(t.Context(), "How much would the bench cost?", opts)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := drainStream(t, stream)
			result := <-stream.Done
			if got != tc.want || result.Recovered != tc.degraded || (result.Err != nil) != tc.degraded {
				t.Fatalf("spoken=%q result=%+v", got, result)
			}
			if len(requests) != tc.calls {
				t.Fatalf("requests=%d want=%d", len(requests), tc.calls)
			}
			history := o.History()
			if tc.want == "" {
				if len(history) != 3 {
					t.Fatalf("failure invented assistant history: %+v", history)
				}
			} else if len(history) != 4 || history[3].Content != tc.want || history[3].ResponseID != "new" {
				t.Fatalf("unexpected history: %+v", history)
			}
			if len(requests) > 1 {
				check := requests[1]
				if len(check.Tools) != 0 || check.Stream == nil || *check.Stream || len(check.Format) == 0 {
					t.Fatal("intent check is not private structured inference")
				}
				if !strings.Contains(check.Messages[1].Content, "How much would the bench cost?") {
					t.Fatal("intent check lost latest request")
				}
			}
			if len(requests) > 2 {
				retry := requests[2]
				if len(retry.Tools) != 0 || retry.Messages[len(retry.Messages)-1].Content != repetitionRetryInstruction {
					t.Fatal("retry must be tools-free with a transient instruction")
				}
				copies := 0
				for _, m := range retry.Messages {
					copies += strings.Count(m.Content, gardenReply)
				}
				if copies != 1 {
					t.Fatalf("rejected attempt entered retry context: %d copies", copies)
				}
			}
		})
	}
}

func TestRepeatedReplyCancellationAndProviderErrorDoNotLeakHeldText(t *testing.T) {
	for _, stage := range []string{"generation", "classification", "generation error"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: gardenReply}})
					w.(http.Flusher).Flush()
					if stage == "generation error" {
						_, _ = fmt.Fprintln(w, `{"error":"connection interrupted"}`)
						return
					}
					if stage == "generation" {
						cancel()
						return
					}
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
					return
				}
				cancel()
			})
			stream, err := o.ThinkStream(ctx, "What about the cost?", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := drainStream(t, stream)
			result := <-stream.Done
			if stage == "generation error" {
				if got != "" || !result.Recovered || result.Err == nil || len(o.History()) != 3 {
					t.Fatalf("got=%q result=%+v history=%+v", got, result, o.History())
				}
			} else if got != "" || !errors.Is(result.Err, context.Canceled) || len(o.History()) != 3 {
				t.Fatalf("got=%q result=%+v history=%+v", got, result, o.History())
			}
		})
	}
}

func TestFreshReplyStreamsBeforeProviderCompletion(t *testing.T) {
	release := make(chan struct{})
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "The bench can face west. "}})
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Keep the path clear."}, Done: true, DoneReason: "stop"})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := o.ThinkStream(ctx, "Where should it face?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-stream.Chunks:
		if first != "The bench can face west. " {
			t.Fatalf("first=%q", first)
		}
		close(release)
	case <-ctx.Done():
		t.Fatal("fresh output was held until model completion")
	}
	rest, _ := drainStream(t, stream)
	result := <-stream.Done
	if rest != "Keep the path clear." || result.Err != nil {
		t.Fatalf("rest=%q result=%+v", rest, result)
	}
}

func TestReplyPrefixGateWhitespaceAndChunks(t *testing.T) {
	for _, size := range []int{1, 7, 1024} {
		g := replyPrefixGate{prior: normalizeReplyPrefix(gardenReply)}
		reply := strings.ReplaceAll(gardenReply, " ", "\n  ") + " New details."
		for offset := 0; offset < len(reply); offset += size {
			if text := g.feed(reply[offset:min(offset+size, len(reply))]); text != "" {
				t.Fatalf("size=%d leaked %q", size, text)
			}
		}
		if !g.repeat || g.release() != reply {
			t.Fatalf("size=%d failed to preserve whole reply", size)
		}
	}
}

func TestRepetitionCandidateUsesOnlyCurrentPersonaCompletedAnswer(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "veronica", MaxHistory: 20}}
	turns := []Turn{{Role: "assistant", Content: gardenReply, PersonaID: "jarvis"}}
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.previousReplyForRepetition(StreamOptions{}) != "" {
		t.Fatal("another persona's answer became own candidate")
	}
	turns = append(turns, Turn{Role: "assistant", Content: gardenReply, PersonaID: "veronica"})
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.previousReplyForRepetition(StreamOptions{}) != gardenReply {
		t.Fatal("own completed answer missing")
	}
	for _, opts := range []StreamOptions{{Background: true}, {TransientHistory: true}, {WorkerBudget: NewWorkerBudget(BudgetLimits{})}} {
		if o.previousReplyForRepetition(opts) != "" {
			t.Fatal("internal turn guarded")
		}
	}
	for _, reply := range []string{"Yes.", "[incomplete response] " + gardenReply} {
		turns = append(turns, Turn{Role: "assistant", Content: reply, PersonaID: "veronica"})
		if err := o.ImportHistory(turns); err != nil {
			t.Fatal(err)
		}
		if o.previousReplyForRepetition(StreamOptions{}) != "" {
			t.Fatalf("ineligible answer guarded: %q", reply)
		}
	}
}

func TestRepetitionRecoveryDoesNotExecuteAttemptedTools(t *testing.T) {
	for _, retryTools := range []bool{false, true} {
		t.Run(fmt.Sprint(retryTools), func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				msg := api.Message{Role: "assistant"}
				switch calls {
				case 1:
					msg.Content = gardenReply
					msg.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "this-must-never-execute"})}}}
				case 2:
					msg.Content = `{"decision":"unrequested"}`
				case 3:
					if retryTools {
						msg.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "this-must-never-execute"})}}}
					} else {
						msg.Content = "The bench should face west."
					}
				default:
					t.Error("unexpected request")
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true, DoneReason: "stop"})
			})
			toolCalls := 0
			stream, err := o.ThinkStream(t.Context(), "Where should the bench face?", StreamOptions{ToolsEnabled: true, OnToolStart: func(ToolCall) { toolCalls++ }})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := drainStream(t, stream)
			result := <-stream.Done
			if toolCalls != 0 || calls != 3 || strings.Contains(got, gardenReply) {
				t.Fatalf("tools=%d calls=%d spoken=%q", toolCalls, calls, got)
			}
			if (result.Err != nil) != retryTools {
				t.Fatalf("result=%+v", result)
			}
			for _, turn := range o.History() {
				if len(turn.ToolCalls) > 0 || turn.Role == "tool" {
					t.Fatalf("rejected tool persisted: %+v", turn)
				}
			}
		})
	}
}

func TestExplicitAndAmbiguousRepeatsBypassClassifier(t *testing.T) {
	for _, input := range []string{"Please say that again.", "Repeat the garden plan and add a budget.", "Quote your last answer exactly.", "Could you run that by me once more?", "Yes.", strings.Repeat("more context ", 200)} {
		t.Run(input[:min(40, len(input))], func(t *testing.T) {
			calls := 0
			o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 {
					t.Error("possible deliberate repeat must not be overridden by classifier")
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: gardenReply}, Done: true, DoneReason: "stop"})
			})
			stream, err := o.ThinkStream(t.Context(), input, StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := drainStream(t, stream)
			result := <-stream.Done
			if got != gardenReply || result.Err != nil || calls != 1 {
				t.Fatalf("got=%q result=%+v calls=%d", got, result, calls)
			}
		})
	}
}

func TestRepeatedHandoffChecksLatestAgentMessage(t *testing.T) {
	calls := 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		reply := gardenReply
		if calls == 2 {
			if !strings.Contains(req.Messages[1].Content, "Veronica, what about the bench cost?") || strings.Contains(req.Messages[1].Content, "synthetic") {
				t.Error("handoff intent must use actual agent message")
			}
			reply = `{"decision":"unrequested"}`
		} else if calls == 3 {
			reply = "The bench costs one hundred dollars."
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: reply}, Done: true, DoneReason: "stop"})
	})
	history := append(o.History(), Turn{Role: "assistant", Content: "Veronica, what about the bench cost?", PersonaID: "jarvis", PersonaDisplayName: "Jarvis"})
	if err := o.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	stream, err := o.ThinkStream(t.Context(), "synthetic continuation", StreamOptions{AgentContinuation: true, Identity: &TurnIdentity{PersonaID: "veronica"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := drainStream(t, stream)
	result := <-stream.Done
	if got != "The bench costs one hundred dollars." || result.Err != nil || calls != 3 || len(o.History()) != 4 {
		t.Fatalf("got=%q result=%+v calls=%d", got, result, calls)
	}
}
