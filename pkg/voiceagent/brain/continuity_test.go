package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

func continuityConfig() *config.Config {
	return &config.Config{MaxHistory: 10, OllamaNumCtx: 8192, ContinuityProjectionEnabled: true, ContinuityArchiveEnabled: true, ContinuityIntegrityEnabled: true, ContinuityRecoveryMax: 2, ContinuitySoftLimitRatio: 0.8, ContinuityNumCtxCeil: 8192}
}

func TestToolProjectionBothLoopsAndSessionRoundtrip(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "full"}[streaming], func(t *testing.T) {
			dir := t.TempDir()
			raw := `{"id":"WI-b94dad","title":"context continuity","status":"active","body":"` + strings.Repeat("payload", 3000) + `"}`
			if err := os.WriteFile(filepath.Join(dir, "camp.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if requests.Add(1) == 1 {
					_, _ = io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"id":"call-1","function":{"name":"read_file","arguments":{"path":"camp.json"}}}]},"done":true}`+"\n")
					return
				}
				tool := req.Messages[len(req.Messages)-1]
				if tool.Role != "tool" || tool.ToolCallID != "call-1" || tool.ToolName != "read_file" || len(tool.Content) > continuity.NoteBytes || !strings.Contains(tool.Content, "WI-b94dad") || strings.Contains(tool.Content, strings.Repeat("payload", 100)) {
					t.Errorf("unprojected or unpaired tool message: %+v", tool)
				}
				_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"WI-b94dad is active."},"done":true,"done_reason":"stop"}`+"\n")
			}))
			defer srv.Close()
			base, _ := url.Parse(srv.URL)
			o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), workDir: dir, model: "m"}
			if streaming {
				stream, err := o.ThinkStream(t.Context(), "Read camp.json", StreamOptions{ToolsEnabled: true})
				if err != nil {
					t.Fatal(err)
				}
				drainStream(t, stream)
				if result := <-stream.Done; result.Err != nil {
					t.Fatal(result.Err)
				}
			} else {
				if _, err := o.ThinkFull(t.Context(), "Read camp.json", StreamOptions{ToolsEnabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			// The canonical transcript intentionally retains raw tool data;
			// the captured requests above assert the bounded native projection.
			saved, _ := json.Marshal(o.CanonicalHistory())
			if !strings.Contains(string(saved), strings.Repeat("payload", 100)) {
				t.Fatal("raw payload missing from canonical transcript")
			}
			var turns []Turn
			if err := json.Unmarshal(saved, &turns); err != nil {
				t.Fatal(err)
			}
			restored := &OllamaBrain{cfg: continuityConfig()}
			restored.LoadHistory(turns)
			if len(restored.History()) != len(turns) {
				t.Fatal("tool pairing lost during roundtrip")
			}
			if len(o.ContinuityArchive()) != 1 || o.ContinuityArchive()[0].Raw == "" {
				t.Fatal("missing raw archive")
			}
		})
	}
}

func TestContinuityRecoveryCompletesOrExplicitlyDegrades(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(map[bool]string{false: "continued", true: "exhausted"}[exhaust], func(t *testing.T) {
			var chats atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Stream != nil && !*req.Stream {
					_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"The user asks about WI-b94dad."},"done":true,"done_reason":"stop"}`+"\n")
					return
				}
				n := chats.Add(1)
				if n == 1 || exhaust {
					_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"The next task is"},"done":true,"done_reason":"length","eval_count":4}`+"\n")
				} else {
					_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":" context continuity."},"done":true,"done_reason":"stop","eval_count":4}`+"\n")
				}
			}))
			defer srv.Close()
			base, _ := url.Parse(srv.URL)
			o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), model: "m"}
			var recoveries atomic.Int32
			o.SetContinuitySink(func(e events.Event) {
				if _, ok := e.(events.ContinuityRecovery); ok {
					recoveries.Add(1)
				}
			})
			stream, err := o.ThinkStream(t.Context(), "What next?", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			spoken, _ := drainStream(t, stream)
			result := <-stream.Done
			if recoveries.Load() != chats.Load()-1 {
				t.Fatalf("recovery telemetry=%d requests=%d", recoveries.Load(), chats.Load())
			}
			if strings.Contains(spoken, "Continuing.") {
				t.Fatalf("recovery announcement leaked into speech: %q", spoken)
			}
			if exhaust {
				if chats.Load() != 3 || !result.Recovered || result.Err == nil || strings.Contains(spoken, oldContinuityFallback) {
					t.Fatalf("unbounded or silent failure: requests=%d result=%+v text=%q", chats.Load(), result, spoken)
				}
			} else if result.Err != nil || !strings.Contains(spoken, "context continuity.") || chats.Load() != 2 {
				t.Fatalf("recovery failed: %+v %q", result, spoken)
			}
		})
	}
}

func TestContinuityBudgetArchivesAndExpandsOnlyWhenOptedIn(t *testing.T) {
	cfg := continuityConfig()
	o := &OllamaBrain{cfg: cfg}
	for i := 0; i < 15; i++ {
		o.history = append(o.history, api.Message{Role: "user", Content: "remember WI-b94dad"}, api.Message{Role: "assistant", Content: "noted"})
		o.trimHistory()
	}
	if len(o.history) > 12 || len(o.ContinuityArchive()) == 0 {
		t.Fatal("hot/archive bounds not enforced")
	}
	o.history = append(o.history, api.Message{Role: "user", Content: strings.Repeat("x", 30000)})
	if !o.NeedsContinuityCompact("") {
		t.Fatal("token pressure missed")
	}
	o.ensureContextBudget("")
	if o.effectiveNumCtx() != 8192 {
		t.Fatal("default expanded before hardware acceptance")
	}
	cfg.ContinuityNumCtxCeil = 32768
	o.ensureContextBudget("")
	if o.effectiveNumCtx() != 16384 || o.warmupNumCtx(o.contextHighWater) != 16384 || o.requestOptions()["num_ctx"] != 16384 {
		t.Fatal("sticky warmup/chat options diverge")
	}
	o.ClearHistory()
	if len(o.ContinuityArchive()) != 0 {
		t.Fatal("clear history retained archive")
	}
}

func TestUnsendablePromptNeverReachesServer(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), model: "m"}
	stream, err := o.ThinkStream(context.Background(), strings.Repeat("x", 40000), StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spoken, _ := drainStream(t, stream)
	res := <-stream.Done
	if requests.Load() != 0 || !res.Recovered || res.Err == nil || spoken != "" {
		t.Fatalf("unsafe send or missing error: %+v %q", res, spoken)
	}
}

func TestEvictedWorkitemCanBeRecalledAndCompacted(t *testing.T) {
	o := &OllamaBrain{cfg: continuityConfig()}
	o.history = []api.Message{{Role: "user", Content: "Track WI-b94dad context continuity"}, {Role: "assistant", Content: "Its status is active."}}
	for i := 0; i < 8; i++ {
		o.history = append(o.history, api.Message{Role: "user", Content: "another turn"}, api.Message{Role: "assistant", Content: "fine."})
		o.trimHistory()
	}
	if strings.Contains(o.history[0].Content, "WI-b94dad") {
		t.Fatal("fixture did not evict workitem")
	}
	o.history = append(o.history, api.Message{Role: "user", Content: "What was WI-b94dad about?"})
	messages := o.buildMessages("")
	found := false
	for _, m := range messages {
		if m.Role == "tool" && m.ToolName == "recall_context" && strings.Contains(m.Content, "WI-b94dad") {
			found = true
		}
	}
	if !found {
		t.Fatal("evicted reference absent from follow-up request")
	}
	// Ordinary followups do not trigger archive search; compaction still promotes
	// the recent summaries as tool data, never as user/system instructions.
	o.history[len(o.history)-1].Content = "Summarize the conversation."
	messages = o.compactionMessages("")
	found = false
	for _, m := range messages {
		if m.Role == "tool" && m.ToolName == "recall_context" && strings.Contains(m.Content, "WI-b94dad") {
			found = true
		}
	}
	if !found {
		t.Fatal("archive omitted from compact request")
	}
}

func TestCancelDuringContinuationCompactStopsWithoutFallback(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Stream != nil && !*req.Stream {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"The answer is"},"done":true,"done_reason":"length"}`+"\n")
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), model: "m"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := o.ThinkStream(ctx, "continue", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { <-started; cancel() }()
	spoken, _ := drainStream(t, stream)
	result := <-stream.Done
	if result.Err == nil || strings.Contains(spoken, oldContinuityFallback) || strings.Contains(spoken, oldRecoveryReply) {
		t.Fatalf("spoke after cancellation: %+v %q", result, spoken)
	}
}

func TestReadSkillPreservesFullInstructionsOnlyForActiveTurn(t *testing.T) {
	const tail = "Required instruction at the end: report WI-b94dad."
	body := strings.Repeat("skill context ", 250) + tail
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"id":"skill-1","function":{"name":"read_skill","arguments":{"name":"continuity"}}}]},"done":true}`+"\n")
			return
		}
		found := false
		for _, m := range req.Messages {
			if m.Role == "user" && strings.Contains(m.Content, tail) {
				found = true
			}
			if m.Role == "tool" && len(m.Content) > continuity.NoteBytes {
				t.Error("unprojected skill history")
			}
		}
		if !found {
			t.Error("skill tail omitted from active request")
		}
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"WI-b94dad is active."},"done":true,"done_reason":"stop"}`+"\n")
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), model: "m", skills: []skills.Skill{{Name: "continuity", Body: body}}}
	stream, err := o.ThinkStream(t.Context(), "Use the continuity skill", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatal(res.Err)
	}
	history, _ := json.Marshal(o.CanonicalHistory())
	if !strings.Contains(string(history), tail) {
		t.Fatal("full skill body missing from canonical transcript")
	}
}

func TestContinuationPreflightIncludesItsInstructionAndToolSchemas(t *testing.T) {
	o := &OllamaBrain{cfg: continuityConfig()}
	// 7372 tokens is the 90% cap for 8192. Existing history fits, but the
	// extra continuation must fail before an HTTP request can truncate it.
	messages := []api.Message{{Role: "user", Content: strings.Repeat("x", 29440)}}
	if err := o.messagesBudgetError(messages, nil); err != nil {
		t.Fatal(err)
	}
	messages = append(messages, api.Message{Role: "user", Content: strings.Repeat("continue ", 50)})
	if err := o.messagesBudgetError(messages, nil); err == nil {
		t.Fatal("continuation instruction omitted from budget")
	}
	tools := api.Tools{{Type: "function", Function: api.ToolFunction{Name: "large_schema", Description: strings.Repeat("schema", 100)}}}
	if err := o.messagesBudgetError(messages[:1], tools); err == nil {
		t.Fatal("tool schema omitted from budget")
	}
}

func TestBudgetShrinkPreservesProjectedUTF8(t *testing.T) {
	raw := strings.Repeat("日本語", 1000)
	for limit := len(toolResultTruncationMarker); limit < 80; limit++ {
		clipped := truncateToBytes(raw, limit)
		if len(clipped) > limit || !utf8.ValidString(clipped) {
			t.Fatalf("invalid truncated result at %d: %q", limit, clipped)
		}
	}
}

func TestSessionRestoreClearsArchiveWhileCompactionPreservesIt(t *testing.T) {
	o := &OllamaBrain{cfg: continuityConfig()}
	o.history = []api.Message{{Role: "user", Content: "Session A secret WI-private"}, {Role: "assistant", Content: "Recorded."}}
	o.archiveMessages(o.history)
	o.ReseedContinuity([]Turn{{Role: "assistant", Content: "Summary of session A."}, {Role: "user", Content: "What did we discuss earlier?"}})
	if !strings.Contains(o.recallNotes(), "WI-private") {
		t.Fatal("same-session compact lost archive")
	}
	o.LoadHistory([]Turn{{Role: "user", Content: "Session B: what did we discuss earlier?"}})
	if len(o.ContinuityArchive()) != 0 || o.recallNotes() != "" {
		t.Fatal("session restore retained another session's archive")
	}
	for _, m := range o.buildMessages("") {
		if strings.Contains(m.Content, "WI-private") {
			t.Fatal("session A leaked into session B")
		}
	}
}

func TestCompleteTaggedAnswerDoesNotStartContinuation(t *testing.T) {
	var chats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chats.Add(1)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"All done. [sigh]"},"done":true,"done_reason":"stop","eval_count":12}`+"\n")
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{cfg: continuityConfig(), client: api.NewClient(base, http.DefaultClient), model: "m"}
	stream, err := o.ThinkStream(t.Context(), "Are we done?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spoken, _ := drainStream(t, stream)
	result := <-stream.Done
	if result.Err != nil || result.Integrity.Truncated || result.Recovered || chats.Load() != 1 {
		t.Fatalf("complete tagged answer retried: requests=%d result=%+v", chats.Load(), result)
	}
	if !strings.Contains(spoken, "All done.") || strings.Contains(spoken, "Continuing.") {
		t.Fatalf("unexpected speech: %q", spoken)
	}
	for _, turn := range o.History() {
		if strings.Contains(turn.Content, "[incomplete response]") {
			t.Fatal("complete answer marked incomplete in history")
		}
	}
}
