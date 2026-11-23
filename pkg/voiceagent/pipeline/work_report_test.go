package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/ollama/ollama/api"
)

const reportAnswer = "Read notes.txt; other.txt is not read yet."

// reportAfterFirstTool asks job id for its report when its first tool call
// returns, on the worker's goroutine, before its next request is built.
func (h *steerJobHarness) reportAfterFirstTool(id func() string) *WorkReceipt {
	var once sync.Once
	receipt := new(WorkReceipt)
	events.Subscribe(h.p.Events, func(e events.ToolCallFinished) {
		if e.JobID != "" {
			once.Do(func() { *receipt = h.p.ReportWork(id()) })
		}
	})
	return receipt
}

func (h *steerJobHarness) reports() *[]events.JobReport {
	var got []events.JobReport
	events.Subscribe(h.p.Events, func(e events.JobReport) {
		h.mu.Lock()
		got = append(got, e)
		h.mu.Unlock()
	})
	return &got
}

func (h *steerJobHarness) awaitFailed() events.JobFailed {
	h.t.Helper()
	select {
	case e := <-h.failed:
		return e
	case e := <-h.finished:
		h.t.Fatalf("job finished: %+v", e)
	case <-time.After(10 * time.Second):
		h.t.Fatal("job did not fail")
	}
	return events.JobFailed{}
}

// toolsFree reports a request with no tools that asks for the report.
func toolsFree(req api.ChatRequest) bool {
	last := req.Messages[len(req.Messages)-1]
	return len(req.Tools) == 0 && last.Role == "user" && strings.Contains(last.Content, "Stop here and say what is done")
}

// After a report the next request has no tools and asks for the answer, which
// is delivered cut short and saved to the conversation as unfinished.
func TestReportedJobIsDeliveredCutShort(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "inbox"}[inbox], func(t *testing.T) {
			h := newSteerJobHarnessWith(t, steerSetup{inbox: inbox}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
				if n == 1 {
					return readCall("notes.txt"), 0
				}
				if !toolsFree(req) {
					t.Errorf("request %d after the report offered tools or no instruction", n)
				}
				return textReply(reportAnswer), 0
			})
			reports := h.reports()
			receipt := h.reportAfterFirstTool(h.lastAccepted)
			id := h.spawn("Read notes.txt and other.txt in the fixture folder")
			h.setAccepted(id)
			if inbox {
				outcome := h.awaitOutcome(id)
				if outcome.Status != work.OutcomePartial || outcome.Limitations != "cut short: report requested" || outcome.Text != reportAnswer {
					t.Fatalf("outcome=%+v", outcome)
				}
			} else {
				finished := h.awaitFinished(id)
				if !finished.CutShort || finished.CutShortReason != string(brain.ForcedByReport) || finished.Summary != reportAnswer {
					t.Fatalf("delivered %+v", finished)
				}
				if err := h.p.RunBackgroundTurn(t.Context()); err != nil {
					t.Fatal(err)
				}
				saved := h.p.Brain.History()
				if last := saved[len(saved)-1]; last.Content != reportAnswer || last.Disposition != brain.IncompleteDisposition(brain.ForcedByReport) {
					t.Fatalf("saved conversation ends %+v", last)
				}
			}
			if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlAccepted || receipt.Jobs[0].Checkpoint == "" || receipt.Jobs[0].Checkpoint == noActivity {
				t.Fatalf("receipt=%+v", *receipt)
			}
			if got := *reports; len(got) != 1 || got[0].ID != id || !got[0].Answer || got[0].Checkpoint != receipt.Jobs[0].Checkpoint {
				t.Fatalf("job_report events=%+v", got)
			}
			if n := len(h.requests()); n != 2 {
				t.Fatalf("requests=%d", n)
			}
			info := onlyWorker(t, h.p)
			if info.Report != reportAsked || info.Checkpoint != receipt.Jobs[0].Checkpoint {
				t.Fatalf("workers row=%+v", info)
			}
		})
	}
}

// The report arrives while a request is in flight: the checkpoint comes back
// at once, the request finishes, its calls are not run, and the job reads as
// reporting and running until the tools-free round answers.
func TestReportWhileARequestIsInFlight(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return readCall("notes.txt"), 0
		case 2:
			close(inFlight)
			<-release
			return readCall("other.txt"), 0
		}
		if !toolsFree(req) {
			t.Error("the request after the report offered tools")
		}
		return textReply(reportAnswer), 0
	})
	id := h.spawn("Read notes.txt and other.txt in the fixture folder")
	<-inFlight
	receipt := h.p.ReportWork(id)
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlAccepted || receipt.Jobs[0].Checkpoint == noActivity {
		t.Fatalf("receipt=%+v", receipt)
	}
	if info := onlyWorker(t, h.p); info.State != string(job.StateRunning) || info.Report != reportRunning {
		t.Fatalf("workers row while reporting=%+v", info)
	}
	if got := h.table.Status(); got[0].State != job.StateRunning {
		t.Fatalf("table state while reporting=%q", got[0].State)
	}
	close(release)
	finished := h.awaitFinished(id)
	if finished.CutShortReason != string(brain.ForcedByReport) {
		t.Fatalf("delivered %+v", finished)
	}
	info := onlyWorker(t, h.p)
	var notRun int
	for _, turn := range info.Thread {
		if turn.Role == "tool" && turn.Content == brain.ToolNotRunReport {
			notRun++
		}
		if turn.Role == "tool" && strings.Contains(turn.Content, "contents of other.txt") {
			t.Fatal("a call the report kept from running was run")
		}
	}
	if notRun != 1 || len(h.requests()) != 3 {
		t.Fatalf("not run=%d requests=%d", notRun, len(h.requests()))
	}
}

func TestSecondReportChangesNothing(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return readCall("notes.txt"), 0
		case 2:
			close(inFlight)
			<-release
			return readCall("other.txt"), 0
		}
		return textReply(reportAnswer), 0
	})
	reports := h.reports()
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	first := h.p.ReportWork(id)
	second := h.p.ReportWork(id)
	if !slices.Equal(first.Jobs, second.Jobs) || first.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	close(release)
	h.awaitFinished(id)
	if len(*reports) != 1 || len(h.requests()) != 3 {
		t.Fatalf("events=%d requests=%d", len(*reports), len(h.requests()))
	}
}

// The job's own final answer is sealed while the report is pending: it is
// delivered as it is, not cut short.
func TestJobFinishingBeforeItsReportIsNotCutShort(t *testing.T) {
	var h *steerJobHarness
	var receipt WorkReceipt
	h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		receipt = h.p.ReportWork(h.lastAccepted())
		return textReply("notes.txt lists three action items."), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	h.setAccepted(id)
	finished := h.awaitFinished(id)
	if finished.CutShort || finished.Summary != "notes.txt lists three action items." || len(h.requests()) != 1 {
		t.Fatalf("delivered %+v after %d requests", finished, len(h.requests()))
	}
	if receipt.Jobs[0].Outcome != ControlAccepted || receipt.Jobs[0].Checkpoint != noActivity {
		t.Fatalf("receipt=%+v", receipt)
	}
	if info := onlyWorker(t, h.p); info.Report != reportEarly || info.Checkpoint != noActivity {
		t.Fatalf("workers row=%+v", info)
	}
}

// A correction waiting when the report is accepted cannot apply; one a request
// already carries is left to be included; a tell afterwards is refused.
func TestReportSettlesCorrections(t *testing.T) {
	var h *steerJobHarness
	var reported, after WorkReceipt
	h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			h.p.TellWork(h.lastAccepted(), "First: read other.txt too.")
			return readCall("notes.txt"), 0
		case 2:
			h.p.TellWork(h.lastAccepted(), "Second: and skip the summary.")
			reported = h.p.ReportWork(h.lastAccepted())
			after = h.p.TellWork(h.lastAccepted(), "Third: too late.")
			return readCall("other.txt"), 0
		}
		return textReply(reportAnswer), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	h.setAccepted(id)
	h.awaitFinished(id)
	if reported.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("report=%+v", reported)
	}
	if after.Jobs[0].Outcome != ControlRejected || after.Jobs[0].Reason != "the job is reporting" {
		t.Fatalf("tell after the report=%+v", after)
	}
	all := h.a.steerFor(id).control.Corrections()
	if len(all) != 2 || all[0].State != brain.CorrectionIncluded || all[1].State != brain.CorrectionNotApplied || all[1].Reason != brain.NotAppliedReport {
		t.Fatalf("corrections=%+v", all)
	}
	if userCount(h.requests()[2], "Second: and skip the summary.") != 0 {
		t.Fatal("a correction the report settled reached the worker")
	}
}

// A cancel ends a reporting job as it ends any job: its receipt is a cancel's
// and the checkpoint stays readable.
func TestCancelDuringTheReportAnswer(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return readCall("notes.txt"), 0
		}
		once.Do(func() { close(inFlight) })
		<-release
		return textReply("Too late."), 0
	})
	defer close(release)
	receipt := h.reportAfterFirstTool(func() string { return "job-1" })
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	cancel := h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkCancel, JobID: id})
	item := cancel.Jobs[0]
	if (item.Outcome != ControlStopped && item.Outcome != ControlStopping) || item.Checkpoint != "" || item.Queued || cancel.Err() != nil {
		t.Fatalf("cancel receipt=%+v", cancel)
	}
	if line := phraseCancelLine(cancel); line != "Stopped." && line != "Cancelled; the worker is still stopping." {
		t.Fatalf("cancel line=%q", line)
	}
	select {
	case e := <-h.aborted:
		if e.Reason != events.AbortCancelled {
			t.Fatalf("aborted=%+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job was not stopped")
	}
	if h.table.Active() {
		t.Fatal("the reporting job kept its slot")
	}
	info := onlyWorker(t, h.p)
	if info.Checkpoint == "" || info.Checkpoint != receipt.Jobs[0].Checkpoint || info.Report != reportAsked {
		t.Fatalf("workers row after the cancel=%+v", info)
	}
}

func TestEmptyReportAnswerFailsTheJob(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return readCall("notes.txt"), 0
		}
		return textReply(""), 0
	})
	receipt := h.reportAfterFirstTool(func() string { return "job-1" })
	h.spawn("Read notes.txt in the fixture folder")
	failed := h.awaitFailed()
	if !strings.Contains(failed.Err, "model returned no final answer") {
		t.Fatalf("failed=%+v", failed)
	}
	if info := onlyWorker(t, h.p); info.Checkpoint == "" || info.Checkpoint != receipt.Jobs[0].Checkpoint {
		t.Fatalf("workers row=%+v", info)
	}
}

// A job asked for a picture and then for its report answers in words: no
// repair is asked for and no missing picture fails it. A picture it did
// attach is still delivered.
func TestReportedPictureJob(t *testing.T) {
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	t.Run("words only", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport, JobID: "job-1"})
				return readCall("notes.txt"), 0
			}
			return textReply("The chart is not drawn yet; notes.txt holds the figures."), 0
		})
		result, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10}))
		if err != nil || result != "The chart is not drawn yet; notes.txt holds the figures." {
			t.Fatalf("result=%q err=%v", result, err)
		}
		if reqs := h.requests(); len(reqs) != 2 || repairRequests(reqs) != 0 {
			t.Fatalf("requests=%d repairs=%d", len(reqs), repairRequests(reqs))
		}
		h.a.mu.Lock()
		origin := h.a.workerOrigins["job-1"]
		h.a.mu.Unlock()
		if brain.ForcedAnswerReason(origin.incomplete) != string(brain.ForcedByReport) || origin.noImage {
			t.Fatalf("origin=%+v", origin)
		}
	})
	t.Run("picture attached", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport, JobID: "job-1"})
				return readCall("notes.txt"), 0
			}
			return textReply("The chart so far.\n![Chart](<" + filepath.Join(h.root, "chart.png") + ">)"), 0
		})
		if err := os.WriteFile(filepath.Join(h.root, "chart.png"), testmedia.PNG(), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10})); err != nil {
			t.Fatal(err)
		}
		if len(h.a.workerReplyMedia("job-1")) != 1 || repairRequests(h.requests()) != 0 {
			t.Fatal("the attached picture was not delivered")
		}
	})
}

func TestReportReceipts(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) { return textReply("ran"), 0 })
		release := h.holdSlot()
		defer release()
		<-h.queued
		reports := h.reports()
		id := h.spawn("Read notes.txt in the fixture folder")
		r := h.p.ReportWork(id)
		if got := r.Jobs[0]; got.Outcome != ControlRejected || !got.Queued || got.Reason != reportNotStarted || got.Checkpoint != "" {
			t.Fatalf("receipt=%+v", r)
		}
		if state := h.a.jobState(id); state != job.StateQueued || h.a.steerFor(id).reporting() || len(*reports) != 0 {
			t.Fatalf("a refused report touched the queued job: %q", state)
		}
	})
	t.Run("worker that cannot take input", func(t *testing.T) {
		h := newControlHarness(t, stubbornHookWorker{release: make(chan struct{})}, false)
		var reports []events.JobReport
		events.Subscribe(h.agency.pipeline.Events, func(e events.JobReport) { reports = append(reports, e) })
		h.spawn("list the files here")
		<-h.tool
		id := h.agency.jobs.Status()[0].ID
		r := h.agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport, JobID: id})
		got := r.Jobs[0]
		if got.Outcome != ControlRejected || got.Reason != reportNoInput || got.Checkpoint == "" || got.Checkpoint == noActivity {
			t.Fatalf("receipt=%+v", r)
		}
		if len(reports) != 1 || reports[0].Answer || reports[0].Checkpoint != got.Checkpoint {
			t.Fatalf("job_report events=%+v", reports)
		}
		if state := h.agency.jobState(id); state != job.StateRunning || h.agency.steerFor(id).reporting() {
			t.Fatalf("a checkpoint-only report touched the job: %q", state)
		}
	})
	t.Run("finished, unknown and refused", func(t *testing.T) {
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) { return textReply("Done."), 0 })
		id := h.spawn("Read notes.txt in the fixture folder")
		h.awaitFinished(id)
		if r := h.p.ReportWork(id); r.Jobs[0].Outcome != ControlFinished {
			t.Fatalf("finished receipt=%+v", r)
		}
		if r := h.p.ReportWork("job-404"); r.Jobs[0].Outcome != ControlUnknown {
			t.Fatalf("unknown receipt=%+v", r)
		}
		for name, c := range map[string]struct {
			actor WorkActor
			req   WorkRequest
		}{
			"tool actor":   {WorkActor{Kind: WorkActorTool}, WorkRequest{Action: WorkReport, JobID: id}},
			"phrase actor": {WorkActor{Kind: WorkActorPhrase}, WorkRequest{Action: WorkReport, JobID: id}},
			"every job":    {WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport, All: true}},
			"no job":       {WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport}},
		} {
			if r := h.a.ControlWork(c.actor, c.req); r.Rejected == "" || len(r.Jobs) != 0 {
				t.Errorf("%s: receipt=%+v", name, r)
			}
		}
	})
	t.Run("another conversation", func(t *testing.T) {
		inFlight, release := make(chan struct{}), make(chan struct{})
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			close(inFlight)
			<-release
			return textReply("Done."), 0
		})
		defer close(release)
		if err := h.p.BeginConversationIdentity("conversation-1"); err != nil {
			t.Fatal(err)
		}
		id := h.spawn("Read notes.txt in the fixture folder")
		<-inFlight
		if err := h.p.ClearConversation(); err != nil {
			t.Fatal(err)
		}
		if r := h.p.ReportWork(id); r.Jobs[0].Outcome != ControlUnknown {
			t.Fatalf("a report crossed the clear: %+v", r)
		}
		if h.a.steerFor(id).reporting() {
			t.Fatal("a report from another conversation was kept")
		}
	})
}
