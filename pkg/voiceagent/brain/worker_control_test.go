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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
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
	})
	h.ctl.Begin(true, "")
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
		answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
		if err != nil || answer != "a.txt holds the contents." || len(h.requests()) != 1 {
			t.Fatalf("answer=%q err=%v requests=%d", answer, err, len(h.requests()))
		}
		// The worker returns its answer; the runner weighs the waiting
		// correction against what is left.
		sealed, limit := h.ctl.Seal(WorkerLimitDue(t.Context(), opts.WorkerBudget, time.Now()))
		if !sealed || limit != ForcedByRequests {
			t.Fatalf("sealed=%v limit=%q", sealed, limit)
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
	h.ctl.Close(NotAppliedReason(err))
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
	h.ctl.Close(NotAppliedReason(err))
	if got := h.dispositions(); got[len(got)-1].State != CorrectionNotApplied || got[len(got)-1].Reason != NotAppliedContext {
		t.Fatalf("dispositions=%+v", got)
	}
}

func TestSealRefusesLaterCorrections(t *testing.T) {
	ctl := NewWorkerControl(nil)
	if c := ctl.Steer("Before the start."); c.Revision != 1 || !c.Brief {
		t.Fatalf("queued correction: %+v", c)
	}
	if texts := ctl.Begin(true, ""); !slices.Equal(texts, []string{"Before the start."}) {
		t.Fatalf("brief corrections=%q", texts)
	}
	if c := ctl.Steer("Use b.txt."); c.Revision != 2 || c.State != CorrectionPending || c.Brief {
		t.Fatalf("running correction: %+v", c)
	}
	if sealed, _ := ctl.Seal(""); sealed {
		t.Fatal("sealed with a correction waiting")
	}
	sealed, limit := ctl.Seal(ForcedByRequests)
	if !sealed || limit != ForcedByRequests || ctl.Corrections()[1].State != CorrectionNotApplied {
		t.Fatalf("sealed=%v limit=%q corrections=%+v", sealed, limit, ctl.Corrections())
	}
	if c := ctl.Steer("Late."); c.State != CorrectionRefused {
		t.Fatalf("late correction: %+v", c)
	}
}

func TestNotifyCanCancelFromItsOwnCallback(t *testing.T) {
	var ctl *WorkerControl
	var mu sync.Mutex
	var states []CorrectionState
	ctl = NewWorkerControl(func(c Correction) {
		mu.Lock()
		states = append(states, c.State)
		mu.Unlock()
		if c.State == CorrectionPending {
			ctl.Supersede()
		}
	})
	ctl.Begin(true, "")
	done := make(chan struct{})
	go func() {
		ctl.Steer("Use b.txt.")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel from the correction's own event deadlocked")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(states, []CorrectionState{CorrectionPending, CorrectionSuperseded}) {
		t.Fatalf("states=%q", states)
	}
}

func TestCorrectionIsIncludedOnlyOnceARequestIsAnswered(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return api.ChatResponse{}, http.StatusBadRequest
	})
	opts := h.opts()
	opts.OnToolEnd = h.steerOnce("Use b.txt.")
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err == nil {
		t.Fatal("a refused request did not fail the run")
	}
	if got := h.dispositions(); len(got) != 1 || got[0].State != CorrectionPending || !h.ctl.Waiting() {
		t.Fatalf("a request that drew no response included the correction: %+v", got)
	}
}

func pairedToolMessages(msgs []api.Message) error {
	open := map[string]bool{}
	for i, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			if len(open) > 0 {
				return fmt.Errorf("message %d starts calls while %v wait for results", i, open)
			}
			for _, c := range m.ToolCalls {
				open[c.ID] = true
			}
		case m.Role == "tool":
			if !open[m.ToolCallID] {
				return fmt.Errorf("tool result %d (%s) does not follow its call", i, m.ToolCallID)
			}
			delete(open, m.ToolCallID)
		case len(open) > 0:
			return fmt.Errorf("message %d (%s) comes between calls %v and their results", i, m.Role, open)
		}
	}
	return nil
}

func TestRecallAfterWorkerCompactionKeepsToolResultsPaired(t *testing.T) {
	payload := "Finding: " + strings.Repeat("tool detail ", 900)
	summaries := 0
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if len(req.Tools) == 0 {
			summaries++
			return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: "Read the first artifacts; each holds a finding."}}, 0
		}
		if n >= 9 {
			return answerResponse("Done."), 0
		}
		return toolResponse([]api.ToolCall{readCallID(fmt.Sprintf("call-%d", n), fmt.Sprintf("big-%d.txt", n%3+1))}), 0
	})
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(filepath.Join(h.o.workDir, fmt.Sprintf("big-%d.txt", i)), []byte(payload+strconv.Itoa(i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Projection and archive both on, as shipped.
	cfg := continuityConfig()
	cfg.IsWorker, cfg.ContinuitySoftLimitRatio = true, 0.5
	h.o.cfg = cfg
	if _, err := h.o.ThinkFull(t.Context(), "Inspect the artifacts.", h.opts()); err != nil {
		t.Fatal(err)
	}
	if summaries == 0 || h.o.ContinuityNotes() == "" {
		t.Fatalf("no compaction with an archive: summaries=%d", summaries)
	}
	recalled := false
	reqs := h.requests()
	for i, req := range reqs {
		for _, m := range req.Messages {
			recalled = recalled || m.ToolCallID == "continuity-recall"
		}
		if err := pairedToolMessages(req.Messages); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if len(req.Tools) == 0 || i == 0 {
			continue
		}
		// The genuine exchange the previous reply asked for is still there,
		// call and result, and the request still ends with it.
		prev := fmt.Sprintf("call-%d", i)
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "tool" || last.ToolCallID != prev {
			t.Fatalf("request %d ends with %+v, not the result of %s", i+1, last, prev)
		}
	}
	if !recalled {
		t.Fatal("no request carried the recalled archive")
	}
}

func TestCompactionLeavesTheLastRequestForTheAnswer(t *testing.T) {
	payload := "Finding: " + strings.Repeat("tool detail ", 900)
	run := func(t *testing.T, maxRequests int, control bool) (string, []api.ChatRequest, error) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			last := req.Messages[len(req.Messages)-1].Content
			switch {
			case strings.Contains(last, "Summarize this worker task"):
				return api.ChatResponse{Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 8}, Message: api.Message{Role: "assistant", Content: "Two findings read."}}, 0
			case len(req.Tools) == 0:
				return answerResponse("Both artifacts hold findings."), 0
			}
			return toolResponse([]api.ToolCall{readCallID(fmt.Sprintf("call-%d", n), fmt.Sprintf("big-%d.txt", n))}), 0
		})
		for i := 1; i <= 4; i++ {
			if err := os.WriteFile(filepath.Join(h.o.workDir, fmt.Sprintf("big-%d.txt", i)), []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cfg := continuityConfig()
		cfg.IsWorker, cfg.ContinuityProjectionEnabled, cfg.ContinuityArchiveEnabled, cfg.ContinuitySoftLimitRatio = true, false, false, 0.5
		h.o.cfg = cfg
		opts := h.opts()
		if !control {
			opts.Control = nil
		}
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: maxRequests})
		answer, err := h.o.ThinkFull(t.Context(), "Inspect the artifacts.", opts)
		return answer, h.requests(), err
	}
	summarized := func(reqs []api.ChatRequest) bool {
		return slices.ContainsFunc(reqs, func(r api.ChatRequest) bool {
			return strings.Contains(r.Messages[len(r.Messages)-1].Content, "Summarize this worker task")
		})
	}
	t.Run("one request left", func(t *testing.T) {
		for _, control := range []bool{false, true} {
			answer, reqs, err := run(t, 3, control)
			if err != nil || answer != "Both artifacts hold findings." || summarized(reqs) {
				t.Fatalf("control=%v answer=%q err=%v summarized=%v", control, answer, err, summarized(reqs))
			}
		}
	})
	t.Run("two requests left", func(t *testing.T) {
		answer, reqs, err := run(t, 4, true)
		if err != nil || answer != "Both artifacts hold findings." || !summarized(reqs) {
			t.Fatalf("with a control: answer=%q err=%v", answer, err)
		}
		// Without one, as before: the summary spends a request, the tools
		// request spends the last, and the answer has none.
		if _, _, err := run(t, 4, false); !errors.Is(err, ErrWorkerBudgetExceeded) {
			t.Fatalf("without a control: err=%v", err)
		}
	})
}

func TestCorrectionDuringOutageBackoffIsSentInsteadOfTheStaleRetry(t *testing.T) {
	wait := waitOutageRetry
	t.Cleanup(func() { waitOutageRetry = wait })
	var h *steerHarness
	waitOutageRetry = func(context.Context) error {
		h.ctl.Steer("Use b.txt.")
		return nil
	}
	h = newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		case 2:
			return api.ChatResponse{}, http.StatusServiceUnavailable
		}
		return answerResponse("Used b.txt."), 0
	})
	opts := h.opts()
	opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 10, MaxRetries: 1})
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
		t.Fatal(err)
	}
	reqs := h.requests()
	if len(reqs) != 3 || countText(reqs[1], "Use b.txt.") != 0 || countText(reqs[2], "Use b.txt.") != 1 {
		t.Fatalf("requests=%d: the stale request was resent", len(reqs))
	}
	if usage := opts.WorkerBudget.Snapshot(); usage.Requests != 3 || usage.Retries != 0 {
		t.Fatalf("the abandoned attempt was charged: %+v", usage)
	}
}

func TestCorrectionMakesARepeatForcedRequestObsolete(t *testing.T) {
	wait := waitOutageRetry
	t.Cleanup(func() { waitOutageRetry = wait })
	var h *steerHarness
	waitOutageRetry = func(context.Context) error {
		h.ctl.Steer("Read b.txt instead.")
		return nil
	}
	h = newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch {
		case n <= 3:
			return toolResponse([]api.ToolCall{readCallID("call", "a.txt")}), 0
		case n == 4:
			return api.ChatResponse{}, http.StatusServiceUnavailable
		}
		return answerResponse("Read b.txt."), 0
	})
	if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", h.opts()); err != nil {
		t.Fatal(err)
	}
	reqs := h.requests()
	if !hasInstruction(reqs[3], toolRepeatAnswerInstruction) {
		t.Fatal("the fourth request was not the repeat-forced answer")
	}
	next := reqs[4]
	if hasInstruction(next, toolRepeatAnswerInstruction) || len(next.Tools) == 0 || countText(next, "Read b.txt instead.") != 1 {
		t.Fatal("the repeat-forced request was resent instead of the corrected one")
	}
}

func TestContinuedRunKeepsToolIDsAndMarksTheReplacedAnswer(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return toolResponse(readFileCall("a.txt")), 0
		case 2:
			h.ctl.Steer("Read b.txt too.")
			return answerResponse("a.txt is read."), 0
		case 3:
			return toolResponse(readFileCall("b.txt")), 0
		}
		return answerResponse("Both are read."), 0
	})
	opts := h.opts()
	var ids []string
	opts.OnToolStart = func(call ToolCall) { ids = append(ids, call.ID) }
	if answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil || answer != "a.txt is read." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if answer, err := h.o.ThinkFull(t.Context(), "", opts); err != nil || answer != "Both are read." {
		t.Fatalf("continued answer=%q err=%v", answer, err)
	}
	if !slices.Equal(ids, []string{"read_file#1", "read_file#2"}) {
		t.Fatalf("hook ids=%q", ids)
	}
	var replaced []Turn
	for _, turn := range h.o.History() {
		if turn.Disposition == DispositionSuperseded {
			replaced = append(replaced, turn)
		}
	}
	if len(replaced) != 1 || replaced[0].Content != "a.txt is read." {
		t.Fatalf("replaced answers=%+v", replaced)
	}
	sent := h.requests()[2].Messages
	if !slices.ContainsFunc(sent, func(m api.Message) bool {
		return m.Role == "assistant" && strings.HasPrefix(m.Content, "[Answer replaced by a later correction")
	}) {
		t.Fatal("the continued request presents the replaced answer as an ordinary reply")
	}
}

func TestContinuedRunKeepsTheJobsSkills(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			h.ctl.Steer("Use b.txt.")
			return answerResponse("a.txt is read."), 0
		}
		return answerResponse("b.txt is read."), 0
	})
	h.o.skills = []skills.Skill{{Name: "planner", Body: "Always cite line numbers.", Dir: h.o.workDir}}
	if _, err := h.o.ThinkFull(WithActivatedSkills(t.Context(), []string{"planner"}), "Read a.txt.", h.opts()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.o.ThinkFull(t.Context(), "", h.opts()); err != nil {
		t.Fatal(err)
	}
	for i, req := range h.requests() {
		if !slices.ContainsFunc(req.Messages, func(m api.Message) bool { return strings.Contains(m.Content, "Always cite line numbers.") }) {
			t.Fatalf("request %d lost the job's skill", i+1)
		}
	}
}

// A foreground reply cut off by its output limit is appended before the
// summary request is built; recalled notes must not push it out.
func TestRecalledNotesKeepAPartialReply(t *testing.T) {
	cfg := continuityConfig()
	o := &OllamaBrain{cfg: cfg, model: "fixture"}
	o.appendMessage(api.Message{Role: "user", Content: "Tell me about the archive."}, nil)
	o.appendMessage(api.Message{Role: "assistant", Content: "[incomplete response] The archive holds"}, nil)
	o.archive.Add(continuity.Entry{Kind: "turn_block", Summary: "Earlier the user asked about notes."})
	messages := o.compactionMessages("")
	recall := slices.IndexFunc(messages, func(m api.Message) bool { return m.ToolCallID == "continuity-recall" })
	if recall < 0 {
		t.Fatal("no recalled notes")
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "The archive holds") {
		t.Fatalf("the partial reply was dropped: %+v", messages)
	}
}

func TestOnThisMachineChecksTheEndpointAndSaysWhenItCannotTell(t *testing.T) {
	remote := &OllamaBrain{cfg: &config.Config{OllamaHost: "http://192.0.2.10:11434", IsWorker: true}, model: "fixture"}
	if local, err := remote.OnThisMachine(t.Context()); local || err != nil {
		t.Fatalf("a worker on another machine: local=%v err=%v", local, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusInternalServerError)
	}))
	defer server.Close()
	host, _ := url.Parse(server.URL)
	unsure := &OllamaBrain{cfg: &config.Config{OllamaHost: server.URL, IsWorker: true}, client: api.NewClient(host, server.Client()), model: "fixture"}
	if _, err := unsure.OnThisMachine(t.Context()); err == nil {
		t.Fatal("a failed metadata request was taken for an answer")
	}
}
