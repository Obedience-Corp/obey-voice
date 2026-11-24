package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

type reportRunner struct {
	*fakeTurnRunner
	receipt pipeline.WorkReceipt
	ids     []string
}

func (r *reportRunner) ReportWork(id string) pipeline.WorkReceipt {
	r.ids = append(r.ids, id)
	return r.receipt
}

func TestWorkReportPrintsTheCheckpointAndWhatHappensNext(t *testing.T) {
	cases := []struct {
		name string
		item pipeline.JobControl
		want string
	}{
		{"accepted", pipeline.JobControl{ID: "job-1", Outcome: pipeline.ControlAccepted, Checkpoint: "read 2 files · ran 1 command"},
			"Report requested. Done so far in job-1: read 2 files · ran 1 command. It stops using tools and writes its answer now."},
		{"worker that cannot be asked", pipeline.JobControl{ID: "job-1", Outcome: pipeline.ControlRejected, Checkpoint: "ran 3 commands", Reason: "this worker cannot be asked to stop, so the work continues"},
			"Done so far in job-1: ran 3 commands. This worker cannot be asked to stop, so the work continues."},
		{"queued", pipeline.JobControl{ID: "job-1", Outcome: pipeline.ControlRejected, Queued: true, Reason: "the job has not started"},
			"job-1 has not started; it stays queued, and /work cancel drops it."},
		{"finished", pipeline.JobControl{ID: "job-1", Outcome: pipeline.ControlFinished},
			"job-1 has already finished; there is nothing to report."},
		{"unknown", pipeline.JobControl{ID: "job-1", Outcome: pipeline.ControlUnknown}, "No running job job-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := sizedConversation(t, 160, 24)
			runner := &reportRunner{fakeTurnRunner: &fakeTurnRunner{}, receipt: pipeline.WorkReceipt{Jobs: []pipeline.JobControl{c.item}}}
			m.deps.runner = runner
			if _, handled := m.submitLocalSlash("/work report job-1"); !handled {
				t.Fatal("/work report was not handled locally")
			}
			if notice := m.items[len(m.items)-1].notice; notice != c.want {
				t.Fatalf("notice = %q", notice)
			}
			if len(runner.ids) != 1 || runner.ids[0] != "job-1" || len(runner.fakeTurnRunner.texts()) != 0 {
				t.Fatalf("ids=%q chat=%q", runner.ids, runner.fakeTurnRunner.texts())
			}
		})
	}
	m := sizedConversation(t, 160, 24)
	runner := &reportRunner{fakeTurnRunner: &fakeTurnRunner{}}
	m.deps.runner = runner
	for _, line := range []string{"/work report", "/work report job-1 now"} {
		m.submitLocalSlash(line)
		if notice := m.items[len(m.items)-1].notice; notice != "usage: /work report job" {
			t.Fatalf("%s: notice = %q", line, notice)
		}
	}
	if len(runner.ids) != 0 {
		t.Fatalf("a malformed report was sent: %q", runner.ids)
	}
}

func TestJobRowShowsReportingUntilTheJobEnds(t *testing.T) {
	m := sizedConversation(t, 160, 24)
	m.ready = true
	step := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }
	row := func() string { return stripANSI(m.items[m.jobItems["job-1"]].sys) }
	step(events.JobStarted{ID: "job-1", Task: "summarize the notes"})
	step(events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"})
	step(events.JobReport{ID: "job-1", Checkpoint: "read 1 file", Answer: true})
	if !strings.HasSuffix(strings.SplitN(row(), "\n", 2)[0], "· reporting") {
		t.Fatalf("row = %q", row())
	}
	// The call that was running when the report came finishes; the job is
	// still writing its report.
	step(events.ToolCallFinished{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}})
	if !strings.Contains(row(), "· reporting") || strings.Contains(row(), "· working") {
		t.Fatalf("row after the running call = %q", row())
	}
	step(events.JobFinished{ID: "job-1", Task: "summarize the notes", Elapsed: 9 * time.Second, CutShort: true, CutShortReason: "report requested", Summary: "Read one file."})
	sealed := stripANSI(m.items[m.completedJobItems["job-1"]].sys)
	if !strings.Contains(sealed, "done in 9s · cut short: report requested") || strings.Contains(sealed, "reporting") {
		t.Fatalf("sealed row = %q", sealed)
	}
	// A report accepted as the job ended arrives after its row was sealed.
	step(events.JobStarted{ID: "job-2", Task: "tidy the notes"})
	step(events.JobFinished{ID: "job-2", Task: "tidy the notes", Elapsed: time.Second, Summary: "Tidied."})
	step(events.JobReport{ID: "job-2", Checkpoint: "no tool activity recorded yet", Answer: true})
	if sealed := stripANSI(m.items[m.completedJobItems["job-2"]].sys); strings.Contains(sealed, "reporting") {
		t.Fatalf("an ended row was redrawn as reporting: %q", sealed)
	}
	// A worker that cannot be asked keeps working.
	step(events.JobStarted{ID: "job-3", Task: "audit the config"})
	step(events.JobReport{ID: "job-3", Checkpoint: "ran 3 commands", Answer: false})
	if r := stripANSI(m.items[m.jobItems["job-3"]].sys); strings.Contains(r, "reporting") {
		t.Fatalf("a checkpoint-only report marked the row: %q", r)
	}
}

type reportingWorkers struct {
	*demoWorkerController
	ids []string
}

func (r *reportingWorkers) ReportWork(id string) pipeline.WorkReceipt {
	r.ids = append(r.ids, id)
	r.items[0].Report, r.items[0].Checkpoint = "reporting", "read 1 file"
	return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: pipeline.ControlAccepted, Checkpoint: "read 1 file"}}}
}

func TestWorkersReportKeyAndCheckpoint(t *testing.T) {
	controller := &reportingWorkers{demoWorkerController: &demoWorkerController{items: []pipeline.WorkerInfo{{ID: "job-1", Task: "summarize the notes", State: "running"}}}}
	m := newWorkers(&config.Config{}, controller)
	m.width, m.height, m.cursor = 140, 24, 2
	if view := stripANSI(m.View()); !strings.Contains(view, "r report") {
		t.Fatalf("view = %q", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if len(controller.ids) != 1 || controller.ids[0] != "job-1" || !strings.Contains(m.message, "Done so far in job-1: read 1 file") {
		t.Fatalf("ids=%q message=%q", controller.ids, m.message)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "summarize the notes · running") || !strings.Contains(view, "· reporting") {
		t.Fatalf("view = %q", view)
	}
	// The checkpoint stays in the task's detail after it ends.
	controller.items[0].State, controller.items[0].Report = "cancelled", "report requested"
	detail := stripANSI(formatWorkerDetail(controller.items[0], 100, false))
	if !strings.Contains(detail, "Report requested · done so far: read 1 file") {
		t.Fatalf("detail = %q", detail)
	}
}

// A tool result that reads like a marker is a skipped call only in a job that
// accepted the report or correction it names.
func TestWorkerThreadReadsMarkersOnlyForAControlledJob(t *testing.T) {
	for _, c := range []struct {
		name, marker string
		item         pipeline.WorkerInfo
		skipped      bool
	}{
		{"report marker, no report", brain.ToolNotRunReport, pipeline.WorkerInfo{}, false},
		{"report marker, reported", brain.ToolNotRunReport, pipeline.WorkerInfo{Report: "reporting"}, true},
		{"correction marker, no correction", brain.ToolNotRunCorrection, pipeline.WorkerInfo{}, false},
		{"correction marker, corrected", brain.ToolNotRunCorrection, pipeline.WorkerInfo{Correction: "correction included"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			item := c.item
			item.ID, item.Task = "job-1", "read the notes"
			item.Thread = []brain.Turn{
				{Role: "user", Content: "read the notes"},
				{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "c", Name: "read_file", Arguments: `{"path":"marker.txt"}`}}},
				{Role: "tool", ToolCallID: "c", ToolName: "read_file", Content: c.marker},
			}
			if !c.skipped {
				item.Tools = []workfold.Call{{ID: "c", Name: "read_file", Status: "succeeded"}}
			}
			var tools []*workerThreadTool
			for _, e := range workerThreadEntries(item) {
				if e.tool != nil {
					tools = append(tools, e.tool)
				}
			}
			if len(tools) != 1 || (tools[0].call.Status == "succeeded") == c.skipped {
				t.Fatalf("tools=%+v", tools)
			}
		})
	}
}
