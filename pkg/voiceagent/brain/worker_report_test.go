package brain

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
)

const reportCheckpoint = "read 1 file"

// reportOnce returns an OnToolEnd hook that asks for the report when the
// first tool call returns: the barrier is the tool's return.
func (h *steerHarness) reportOnce() func(ToolResult) {
	report := sync.OnceFunc(func() {
		if got, _ := h.ctl.Report(reportCheckpoint); got != ReportAccepted {
			h.t.Errorf("report=%q", got)
		}
		h.ctl.Flush()
	})
	return func(ToolResult) { report() }
}

func TestReportMakesTheNextRequestTheAnswerRound(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse("Read a.txt; b.txt is not read yet."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.reportOnce()
	var forced []ForcedAnswer
	opts.OnForcedAnswer = func(f ForcedAnswer) { forced = append(forced, f) }
	answer, err := h.o.ThinkFull(t.Context(), "Read a.txt and b.txt.", opts)
	if err != nil || answer != "Read a.txt; b.txt is not read yet." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if next := reqs[1]; len(next.Tools) != 0 || !hasInstruction(next, toolReportAnswerInstruction) {
		t.Fatalf("the request after the report: tools=%d messages=%+v", len(next.Tools), next.Messages)
	}
	if len(forced) != 1 || forced[0] != ForcedByReport {
		t.Fatalf("forced=%q", forced)
	}
	// A run continued on the same job is answered without tools too.
	h2 := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		return answerResponse("Nothing more was done."), 0
	})
	h2.ctl.Report(reportCheckpoint)
	if _, err := h2.o.ThinkFull(t.Context(), "", h2.opts()); err != nil {
		t.Fatal(err)
	}
	if first := h2.requests()[0]; len(first.Tools) != 0 || !hasInstruction(first, toolReportAnswerInstruction) {
		t.Fatalf("a continued run offered tools: %d", len(first.Tools))
	}
}

func TestReportMidBatchSkipsUnstartedCalls(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt"), readCallID("call-b", "b.txt"), readCallID("call-c", "c.txt")}), 0
		}
		return answerResponse("Read a.txt only."), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.reportOnce()
	if _, err := h.o.ThinkFull(t.Context(), "Read every file.", opts); err != nil {
		t.Fatal(err)
	}
	if len(h.started) != 1 {
		t.Fatalf("tools started after the report: %q", h.started)
	}
	var results []api.Message
	for _, m := range h.requests()[1].Messages {
		if m.Role == "tool" {
			results = append(results, m)
		}
	}
	if len(results) != 3 || results[0].ToolCallID != "call-a" || strings.HasPrefix(results[0].Content, "not run") {
		t.Fatalf("results=%+v", results)
	}
	for i, id := range []string{"call-b", "call-c"} {
		if r := results[i+1]; r.ToolCallID != id || r.ToolName != "read_file" || r.Content != ToolNotRunReport {
			t.Fatalf("skipped call %s answered %+v", id, r)
		}
	}
}

// The report arrives while a request for more tools is in flight: that
// request finishes, its calls are not run, and the next request is the answer.
func TestReportWhileARequestIsInFlight(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			h.ctl.Report(reportCheckpoint)
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse("Nothing was read."), 0
	})
	answer, err := h.o.ThinkFull(t.Context(), "Read a.txt.", h.opts())
	if err != nil || answer != "Nothing was read." || len(h.started) != 0 {
		t.Fatalf("answer=%q err=%v started=%q", answer, err, h.started)
	}
	if reqs := h.requests(); len(reqs) != 2 || len(reqs[1].Tools) != 0 {
		t.Fatalf("requests=%d", len(reqs))
	}
}

// A request for more tools not yet sent when the report arrives is not sent:
// neither a resend after an outage nor a length continuation.
func TestReportIsNeverAnsweredWithTools(t *testing.T) {
	t.Run("outage resend", func(t *testing.T) {
		wait := waitOutageRetry
		t.Cleanup(func() { waitOutageRetry = wait })
		var h *steerHarness
		waitOutageRetry = func(context.Context) error {
			h.ctl.Report(reportCheckpoint)
			return nil
		}
		h = newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			switch n {
			case 1:
				return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
			case 2:
				return api.ChatResponse{}, http.StatusServiceUnavailable
			}
			return answerResponse("Read a.txt."), 0
		})
		opts := h.opts()
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 10, MaxRetries: 1})
		if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
			t.Fatal(err)
		}
		reqs := h.requests()
		if len(reqs) != 3 || len(reqs[2].Tools) != 0 || !hasInstruction(reqs[2], toolReportAnswerInstruction) {
			t.Fatalf("requests=%d: the request for more tools was resent", len(reqs))
		}
	})
	t.Run("length continuation", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				h.ctl.Report(reportCheckpoint)
				return api.ChatResponse{Done: true, DoneReason: "length", Message: api.Message{Role: "assistant", Content: "Reading a.txt, which says"}}, 0
			}
			return answerResponse("a.txt is not read yet."), 0
		})
		if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", h.opts()); err != nil {
			t.Fatal(err)
		}
		reqs := h.requests()
		if len(reqs) != 2 || len(reqs[1].Tools) != 0 || !hasInstruction(reqs[1], toolReportAnswerInstruction) {
			t.Fatalf("requests=%d: the follow-on request kept its tools", len(reqs))
		}
	})
}

// A report changes nothing for a job already in an answer round a limit or
// the repeat guard imposed: that reason stands.
func TestReportDuringAForcedAnswerKeepsItsReason(t *testing.T) {
	t.Run("repeat guard", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n <= 3 {
				return toolResponse([]api.ToolCall{readCallID("call", "a.txt")}), 0
			}
			h.ctl.Report(reportCheckpoint)
			return answerResponse("a.txt kept returning the same lines."), 0
		})
		opts := h.opts()
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
			t.Fatal(err)
		}
		if reqs := h.requests(); len(reqs) != 4 || !hasInstruction(reqs[3], toolRepeatAnswerInstruction) || forced != ForcedByRepeat {
			t.Fatalf("requests=%d forced=%q", len(reqs), forced)
		}
	})
	t.Run("request limit", func(t *testing.T) {
		h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
			}
			h.ctl.Report(reportCheckpoint)
			return answerResponse("Here is what a.txt holds."), 0
		})
		opts := h.opts()
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 2})
		var forced ForcedAnswer
		opts.OnForcedAnswer = func(f ForcedAnswer) { forced = f }
		if _, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts); err != nil {
			t.Fatal(err)
		}
		if reqs := h.requests(); len(reqs) != 2 || forced != ForcedByRequests {
			t.Fatalf("requests=%d forced=%q", len(reqs), forced)
		}
	})
}

func TestEmptyReportAnswerIsAFailure(t *testing.T) {
	h := newSteerHarness(t, nil, func(h *steerHarness, n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return toolResponse([]api.ToolCall{readCallID("call-a", "a.txt")}), 0
		}
		return answerResponse(""), 0
	})
	opts := h.opts()
	opts.OnToolEnd = h.reportOnce()
	_, err := h.o.ThinkFull(t.Context(), "Read a.txt.", opts)
	if err == nil || err.Error() != stopReport+": model returned no final answer" {
		t.Fatalf("err=%v", err)
	}
}

func TestReportStates(t *testing.T) {
	t.Run("once, then the same checkpoint", func(t *testing.T) {
		var changes []Correction
		c := NewWorkerControl(func(item Correction) { changes = append(changes, item) })
		c.Begin(true, "")
		if got, kept := c.Report("read 2 files"); got != ReportAccepted || kept != "read 2 files" {
			t.Fatalf("first=%q %q", got, kept)
		}
		if got, kept := c.Report("read 3 files"); got != ReportRepeated || kept != "read 2 files" {
			t.Fatalf("second=%q %q", got, kept)
		}
		c.Flush()
		if len(changes) != 0 {
			t.Fatalf("a report reported %+v", changes)
		}
	})
	t.Run("corrections", func(t *testing.T) {
		var changes []Correction
		c := NewWorkerControl(func(item Correction) { changes = append(changes, item) })
		c.Begin(true, "")
		c.Steer("Taken already.")
		c.take()
		c.Steer("Still waiting.")
		c.Report(reportCheckpoint)
		c.Flush()
		c.answered()
		states := finalStates(changes)
		if states[1].State != CorrectionIncluded || states[2].State != CorrectionNotApplied || states[2].Reason != NotAppliedReport {
			t.Fatalf("dispositions=%+v", changes)
		}
		if got := c.Steer("After the report."); got.State != CorrectionRejected || got.Reason != "the job is reporting" {
			t.Fatalf("tell after a report: %+v", got)
		}
		c.Close(NotAppliedFailed)
		if got := c.Steer("After the end."); got.State != CorrectionRefused {
			t.Fatalf("tell after the end: %+v", got)
		}
		if kept, ok := c.Reported(); !ok || kept != reportCheckpoint {
			t.Fatalf("checkpoint after the end=%q %v", kept, ok)
		}
	})
	t.Run("repair", func(t *testing.T) {
		reporting := NewWorkerControl(nil)
		reporting.Begin(true, "")
		reporting.Report(reportCheckpoint)
		if reporting.BeginRepair() {
			t.Fatal("a reporting job was repaired")
		}
		repairing := NewWorkerControl(nil)
		repairing.Begin(true, "")
		if !repairing.BeginRepair() {
			t.Fatal("the repair was not admitted")
		}
		if got, _ := repairing.Report(reportCheckpoint); got != ReportFinishing {
			t.Fatalf("report during a repair=%q", got)
		}
		if _, ok := repairing.Reported(); ok {
			t.Fatal("a refused report was kept")
		}
	})
	t.Run("not started, no input, finished", func(t *testing.T) {
		queued := NewWorkerControl(nil)
		if got, _ := queued.Report(reportCheckpoint); got != ReportNotStarted {
			t.Fatalf("queued=%q", got)
		}
		opaque := NewWorkerControl(nil)
		opaque.Begin(false, "cannot")
		if got, kept := opaque.Report(reportCheckpoint); got != ReportNoInput || kept != reportCheckpoint {
			t.Fatalf("no input=%q %q", got, kept)
		}
		finished := NewWorkerControl(nil)
		finished.Begin(true, "")
		if sealed, _ := finished.Seal(""); !sealed {
			t.Fatal("the answer was not sealed")
		}
		if got, _ := finished.Report(reportCheckpoint); got != ReportFinished {
			t.Fatalf("finished=%q", got)
		}
		for _, c := range []*WorkerControl{queued, opaque, finished} {
			if _, ok := c.Reported(); ok {
				t.Fatal("a refused report was kept")
			}
		}
	})
}
