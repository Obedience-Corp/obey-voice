package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// steerHarness runs one worker turn against a fake Ollama server with a
// control attached, recording every request and every disposition.
type steerHarness struct {
	t   *testing.T
	o   *OllamaBrain
	ctl *WorkerControl

	mu      sync.Mutex
	reqs    []api.ChatRequest
	changes []Correction
	started []string
}

type steerReply func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int)

func newSteerHarness(t *testing.T, cfg *config.Config, reply steerReply) *steerHarness {
	t.Helper()
	h := &steerHarness{t: t}
	h.ctl = NewWorkerControl(func(c Correction) {
		h.mu.Lock()
		h.changes = append(h.changes, c)
		h.mu.Unlock()
	}, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		h.mu.Lock()
		h.reqs = append(h.reqs, req)
		n := len(h.reqs)
		h.mu.Unlock()
		if n > 30 {
			t.Error("unbounded worker requests")
			http.Error(w, "loop", http.StatusInternalServerError)
			return
		}
		response, status := reply(h, n, req)
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if cfg == nil {
		cfg = &config.Config{MaxHistory: 100, OllamaNumCtx: 32768, IsWorker: true}
	}
	host, _ := url.Parse(server.URL)
	h.o = &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: dir, cfg: cfg}
	return h
}

func (h *steerHarness) opts() StreamOptions {
	opts := WorkerStreamOptions(true)
	opts.Control = h.ctl
	opts.OnToolStart = func(call ToolCall) {
		h.mu.Lock()
		h.started = append(h.started, call.Summary)
		h.mu.Unlock()
	}
	return opts
}

func (h *steerHarness) requests() []api.ChatRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.reqs)
}

func (h *steerHarness) dispositions() []Correction {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.changes)
}

// steerOnce returns an OnToolEnd hook that gives each text as a correction
// when the first tool call returns: the barrier is the tool's return.
func (h *steerHarness) steerOnce(texts ...string) func(ToolResult) {
	var once sync.Once
	return func(ToolResult) {
		once.Do(func() {
			for _, text := range texts {
				h.ctl.Steer(text)
			}
		})
	}
}

func readCallID(id, path string) api.ToolCall {
	return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "read_file", Arguments: mustArgs(map[string]any{"path": path})}}
}

func userTexts(req api.ChatRequest) []string {
	var out []string
	for _, m := range req.Messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

func countText(req api.ChatRequest, text string) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "user" && m.Content == text {
			n++
		}
	}
	return n
}

func finalStates(changes []Correction) map[int]Correction {
	out := map[int]Correction{}
	for _, c := range changes {
		out[c.Revision] = c
	}
	return out
}

func TestCorrectionIsInTheNextRequestAfterThePairedResults(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse("Read b.txt as asked."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.steerOnce("Use b.txt instead.")
	answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
	if err != nil || answer != "Read b.txt as asked." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	msgs := reqs[1].Messages
	last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if last.Role != "user" || last.Content != "Use b.txt instead." || prev.Role != "tool" || prev.ToolCallID != "call-a" {
		t.Fatalf("correction is not the user turn after the paired result: %+v", msgs)
	}
	if len(reqs[1].Tools) == 0 {
		t.Fatal("the corrected request lost its tools")
	}
	got := h.dispositions()
	if len(got) != 2 || got[0].State != CorrectionPending || got[1].State != CorrectionIncluded || got[1].Revision != 1 {
		t.Fatalf("dispositions=%+v", got)
	}
	var thread []string
	for _, turn := range h.o.History() {
		if turn.Role == "user" {
			thread = append(thread, turn.Content)
		}
	}
	if !slices.Equal(thread, []string{"Read a.txt.", "Use b.txt instead."}) {
		t.Fatalf("history user turns=%q", thread)
	}
}

func TestCorrectionMidBatchSkipsUnstartedCalls(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt"), readCallID("call-b", "b.txt"), readCallID("call-c", "c.txt")}), 0
		}
		return answerResponse("Done with the new plan."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.steerOnce("Stop reading; summarize a.txt only.")
	if _, err := h.o.ThinkFull(t.Context(), "Read every file.", opts); err != nil {
		t.Fatal(err)
	}
	if len(h.started) != 1 {
		t.Fatalf("tools started after the correction: %q", h.started)
	}
	msgs := h.requests()[1].Messages
	var results []api.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			results = append(results, m)
		}
	}
	if len(results) != 3 || results[0].ToolCallID != "call-a" || strings.Contains(results[0].Content, toolNotRunCorrection) {
		t.Fatalf("results=%+v", results)
	}
	for i, id := range []string{"call-b", "call-c"} {
		r := results[i+1]
		if r.ToolCallID != id || r.ToolName != "read_file" || r.Content != toolNotRunCorrection {
			t.Fatalf("skipped call %s answered %+v", id, r)
		}
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != "Stop reading; summarize a.txt only." {
		t.Fatalf("correction does not follow the results: %+v", last)
	}
}

func TestCorrectionsKeepTheirOrderAndDispositions(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse("Followed both corrections."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.steerOnce("First: use b.txt.", "Second: and c.txt.")
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
		t.Fatal(err)
	}
	users := userTexts(h.requests()[1])
	if len(users) < 2 || !slices.Equal(users[len(users)-2:], []string{"First: use b.txt.", "Second: and c.txt."}) {
		t.Fatalf("user turns=%q", users)
	}
	states := finalStates(h.dispositions())
	if len(states) != 2 || states[1].State != CorrectionIncluded || states[2].State != CorrectionIncluded {
		t.Fatalf("dispositions=%+v", h.dispositions())
	}
	if got := h.ctl.Corrections(); len(got) != 2 || got[0].Text != "First: use b.txt." || got[1].Revision != 2 {
		t.Fatalf("corrections=%+v", got)
	}
}

func TestCancelSupersedesAPendingCorrection(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse("Finished."), 0
	})
	opts := h.opts()
	var once sync.Once
	opts.OnToolEnd = func(ToolResult) {
		once.Do(func() {
			h.ctl.Steer("Use b.txt.")
			h.ctl.Supersede()
		})
	}
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
		t.Fatal(err)
	}
	if n := countText(h.requests()[1], "Use b.txt."); n != 0 {
		t.Fatal("a superseded correction reached the worker")
	}
	if got := h.dispositions(); len(got) != 2 || got[1].State != CorrectionSuperseded {
		t.Fatalf("dispositions=%+v", got)
	}
	if c := h.ctl.Steer("Too late."); c.State != CorrectionRefused {
		t.Fatalf("correction after cancel: %+v", c)
	}
}

func TestCorrectionAfterLimitCannotApply(t *testing.T) {
	t.Run("request limit forces the next round", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
			}
			return answerResponse("Here is what a.txt holds."), 0
		})
		opts := h.opts()
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 2})
		opts.OnToolEnd = h.steerOnce("Use b.txt.")
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
		if err != nil || answer == "" || forced != ForcedByRequests {
			t.Fatalf("answer=%q err=%v forced=%q", answer, err, forced)
		}
		last := h.requests()[1]
		if len(last.Tools) != 0 || countText(last, "Use b.txt.") != 0 {
			t.Fatal("the answer round carried tools or the correction")
		}
		if got := h.dispositions(); len(got) != 2 || got[1].State != CorrectionNotApplied || got[1].Reason != NotAppliedLimit {
			t.Fatalf("dispositions=%+v", got)
		}
	})
	t.Run("answer written before the last request", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			h.ctl.Steer("Use b.txt.")
			return answerResponse("a.txt holds the contents."), 0
		})
		opts := h.opts()
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 2})
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
		if err != nil || answer != "a.txt holds the contents." || forced != ForcedByRequests || len(h.requests()) != 1 {
			t.Fatalf("answer=%q err=%v forced=%q requests=%d", answer, err, forced, len(h.requests()))
		}
		if got := h.dispositions(); len(got) != 2 || got[1].State != CorrectionNotApplied {
			t.Fatalf("dispositions=%+v", got)
		}
	})
}

func TestCorrectionLiftsTheRepeatGuardButNotTheTimeLimit(t *testing.T) {
	t.Run("repeat guard", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n <= 3 {
				return toolResponse([]api.ToolCall{readCallID("call", "a.txt")}), 0
			}
			return answerResponse("Read b.txt instead."), 0
		})
		opts := h.opts()
		rounds := 0
		var once sync.Once
		opts.OnToolEnd = func(ToolResult) {
			if rounds++; rounds == 3 {
				once.Do(func() { h.ctl.Steer("Read b.txt instead.") })
			}
		}
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
			t.Fatal(err)
		}
		reqs := h.requests()
		next := reqs[3]
		if len(next.Tools) == 0 || hasInstruction(next, toolRepeatAnswerInstruction) || countText(next, "Read b.txt instead.") != 1 {
			t.Fatalf("corrected request after a repeat trip: tools=%d repeat=%v", len(next.Tools), hasInstruction(next, toolRepeatAnswerInstruction))
		}
		if forced != "" {
			t.Fatalf("answer flagged forced: %q", forced)
		}
	})
	t.Run("time limit", func(t *testing.T) {
		reserve := workerTimeReserve
		t.Cleanup(func() { workerTimeReserve = reserve })
		var mu sync.Mutex
		due := false
		workerTimeReserve = func(total time.Duration) time.Duration {
			mu.Lock()
			defer mu.Unlock()
			if due {
				return total
			}
			return 0
		}
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				return toolResponse([]api.ToolCall{readCallID("call", "a.txt")}), 0
			}
			return answerResponse("a.txt is read; time is nearly up."), 0
		})
		opts := h.opts()
		opts.OnToolEnd = func(ToolResult) {
			mu.Lock()
			due = true
			mu.Unlock()
			h.ctl.Steer("Read b.txt instead.")
		}
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		ctx := WithWorkerDeadline(t.Context(), time.Now().Add(10*time.Minute))
		if _, err := h.o.ThinkFull(ctx, "Read a.txt.", opts); err != nil {
			t.Fatal(err)
		}
		next := h.requests()[1]
		if len(next.Tools) != 0 || !hasInstruction(next, toolTimeAnswerInstruction) || countText(next, "Read b.txt instead.") != 0 || forced != ForcedByTime {
			t.Fatalf("time-limited answer round changed: tools=%d forced=%q", len(next.Tools), forced)
		}
		if got := h.dispositions(); got[len(got)-1].State != CorrectionNotApplied || got[len(got)-1].Reason != NotAppliedLimit {
			t.Fatalf("dispositions=%+v", got)
		}
	})
}

func TestRetriedRequestCarriesACorrectionOnce(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		case 2:
			return api.ChatResponse{}, http.StatusServiceUnavailable
		}
		return answerResponse("Used b.txt."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.steerOnce("Use b.txt.")
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
		t.Fatal(err)
	}
	reqs := h.requests()
	if len(reqs) != 3 || countText(reqs[1], "Use b.txt.") != 1 || countText(reqs[2], "Use b.txt.") != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	included := 0
	for _, c := range h.dispositions() {
		if c.State == CorrectionIncluded {
			included++
		}
	}
	if included != 1 {
		t.Fatalf("included reported %d times", included)
	}
	n := 0
	for _, turn := range h.o.History() {
		if turn.Role == "user" && turn.Content == "Use b.txt." {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("history holds the correction %d times", n)
	}
}

func TestFailedRequestReturnsUnsentCorrectionsToPending(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		response := toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")})
		response.Metrics.EvalCount = 8
		return response, 0
	})
	opts := h.opts()
	// The first reply spends the whole output allowance, so the corrected
	// request is refused before it is sent.
	opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 10, MaxOutputTokens: 8})
	opts.OnToolEnd = h.steerOnce("Use b.txt.")
	_, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
	if !errors.Is(err, ErrWorkerBudgetExceeded) || len(h.requests()) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(h.requests()))
	}
	if len(h.o.History()) != 0 {
		t.Fatal("history was not rolled back")
	}
	if !h.ctl.waiting() {
		t.Fatal("the correction was lost with the rollback")
	}
	h.ctl.Close(NotAppliedReason(t.Context(), err))
	if got := h.dispositions(); len(got) != 2 || got[1].State != CorrectionNotApplied || got[1].Reason != NotAppliedLimit {
		t.Fatalf("dispositions=%+v", got)
	}
}

func TestCorrectionDuringLengthContinuationRestartsTheRequest(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			h.ctl.Steer("Use b.txt.")
			return api.ChatResponse{Done: true, DoneReason: "length", Message: api.Message{Role: "assistant", Content: "Reading a.txt, which says"}}, 0
		}
		return answerResponse("b.txt is read."), 0
	})
	answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", h.opts())
	if err != nil || answer != "b.txt is read." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	msgs := reqs[1].Messages
	if last := msgs[len(msgs)-1]; last.Content != "Use b.txt." {
		t.Fatalf("the follow-on request was a continuation, not the corrected task: %+v", last)
	}
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "which says") {
			t.Fatal("the stale partial answer was kept")
		}
	}
}

func TestCorrectionCompactsAtTheSoftLimitAndKeepsEveryAnchor(t *testing.T) {
	payload := "Finding: " + strings.Repeat("tool detail ", 550)
	summaries := 0
	var once sync.Once
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if len(req.Tools) == 0 {
			summaries++
			once.Do(func() { h.ctl.Steer("Second: write the result to notes.txt.") })
			return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: "Read artifacts 1 to 3; each holds a finding."}}, 0
		}
		tools := 0
		for _, m := range req.Messages {
			if m.Role == "tool" {
				tools++
			}
		}
		if tools < 3 && summaries == 0 {
			return toolResponse([]api.ToolCall{readCallID("call", "big-"+string(rune('1'+tools))+".txt")}), 0
		}
		return answerResponse("Done."), 0
	})
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(filepath.Join(h.o.workDir, "big-"+string(rune('0'+i))+".txt"), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := continuityConfig()
	cfg.IsWorker, cfg.ContinuityProjectionEnabled, cfg.ContinuitySoftLimitRatio = true, false, 0.5
	h.o.cfg = cfg
	opts := h.opts()
	tools := 0
	opts.OnToolEnd = func(ToolResult) {
		if tools++; tools == 1 {
			h.ctl.Steer("First: only report findings.")
		}
	}
	if _, err := h.o.ThinkFull(t.Context(), "Inspect the artifacts.", opts); err != nil {
		t.Fatal(err)
	}
	if summaries == 0 {
		t.Fatal("the worker never compacted")
	}
	reqs := h.requests()
	last := reqs[len(reqs)-1]
	users := userTexts(last)
	want := []string{"Inspect the artifacts.", "First: only report findings.", "Second: write the result to notes.txt."}
	if !slices.Equal(users, want) {
		t.Fatalf("anchors after compaction=%q", users)
	}
	states := finalStates(h.dispositions())
	if states[1].State != CorrectionIncluded || states[2].State != CorrectionIncluded {
		t.Fatalf("dispositions=%+v", h.dispositions())
	}
	// A correction admitted during the summary request is in the very next
	// task request.
	summary := slices.IndexFunc(reqs, func(r api.ChatRequest) bool { return len(r.Tools) == 0 })
	second := "Second: write the result to notes.txt."
	if summary < 0 || summary+1 >= len(reqs) || countText(reqs[summary], second) != 0 || countText(reqs[summary+1], second) != 1 {
		t.Fatalf("summary at %d of %d requests", summary, len(reqs))
	}
}

func TestIrreducibleCorrectionIsAContextLimit(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if len(req.Tools) == 0 {
			return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: "Notes."}}, 0
		}
		return toolResponse([]api.ToolCall{readCallID("call", "a.txt")}), 0
	})
	cfg := continuityConfig()
	cfg.IsWorker, cfg.ContinuityProjectionEnabled, cfg.ContinuitySoftLimitRatio = true, false, 0.5
	h.o.cfg = cfg
	opts := h.opts()
	huge := "Correction: " + strings.Repeat("every word matters here ", 2000)
	opts.OnToolEnd = h.steerOnce(huge)
	_, err := h.o.ThinkFull(t.Context(), "Inspect a.txt.", opts)
	if !errors.Is(err, ErrPromptBudget) {
		t.Fatalf("err=%v", err)
	}
	h.ctl.Close(NotAppliedReason(context.Background(), err))
	if got := h.dispositions(); got[len(got)-1].State != CorrectionNotApplied || got[len(got)-1].Reason != NotAppliedContext {
		t.Fatalf("dispositions=%+v", got)
	}
}

func TestSealRefusesLaterCorrections(t *testing.T) {
	ctl := NewWorkerControl(nil, 2)
	if c := ctl.Steer("Use b.txt."); c.Revision != 3 || c.State != CorrectionPending {
		t.Fatalf("revision after a briefed pair: %+v", c)
	}
	if sealed, _ := ctl.Seal(); sealed {
		t.Fatal("sealed with a correction waiting")
	}
	ctl.setLimit(ForcedByRequests)
	sealed, limit := ctl.Seal()
	if !sealed || limit != ForcedByRequests || ctl.Corrections()[0].State != CorrectionNotApplied {
		t.Fatalf("sealed=%v limit=%q corrections=%+v", sealed, limit, ctl.Corrections())
	}
	if c := ctl.Steer("Late."); c.State != CorrectionRefused {
		t.Fatalf("late correction: %+v", c)
	}
}
