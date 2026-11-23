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
)

type tellRunner struct {
	*fakeTurnRunner
	receipt pipeline.WorkReceipt
	ids     []string
	texts   []string
}

func (r *tellRunner) TellWork(id, text string) pipeline.WorkReceipt {
	r.ids, r.texts = append(r.ids, id), append(r.texts, text)
	return r.receipt
}

func TestWorkTellSendsTheRestOfTheLineAndReportsTheReceipt(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	runner := &tellRunner{fakeTurnRunner: &fakeTurnRunner{}, receipt: pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: "job-1", Outcome: pipeline.ControlAccepted, Revision: 1}}}}
	m.deps.runner = runner
	if _, handled := m.submitLocalSlash(`/work tell job-1 use  the "other" file`); !handled {
		t.Fatal("/work tell was not handled locally")
	}
	if len(runner.ids) != 1 || runner.ids[0] != "job-1" || runner.texts[0] != `use  the "other" file` {
		t.Fatalf("sent ids=%q texts=%q", runner.ids, runner.texts)
	}
	if notice := m.items[len(m.items)-1].notice; !strings.Contains(notice, "Correction sent to job-1") {
		t.Fatalf("notice = %q", notice)
	}
	if len(runner.fakeTurnRunner.texts()) != 0 {
		t.Fatal("the correction reached the conversation as chat")
	}
}

func TestWorkTellReportsAWorkerThatCannotTakeInput(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	runner := &tellRunner{fakeTurnRunner: &fakeTurnRunner{}, receipt: pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: "job-1", Outcome: pipeline.ControlRejected, Reason: "this worker cannot take a correction while it runs"}}}}
	m.deps.runner = runner
	m.submitLocalSlash("/work tell job-1 use the other file")
	if notice := m.items[len(m.items)-1].notice; notice != "Correction not sent to job-1: this worker cannot take a correction while it runs" {
		t.Fatalf("notice = %q", notice)
	}
	m.submitLocalSlash("/work tell job-1")
	if notice := m.items[len(m.items)-1].notice; notice != "usage: /work tell job text" {
		t.Fatalf("notice = %q", notice)
	}
	if len(runner.ids) != 1 {
		t.Fatalf("a correction with no text was sent: %q", runner.ids)
	}
}

func TestJobRowShowsTheCorrectionDisposition(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	step := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }
	step(events.JobStarted{ID: "job-1", Task: "summarize the notes"})
	step(events.JobCorrection{ID: "job-1", Revision: 1, Text: "use the other file", State: "pending"})
	row := func() string { return stripANSI(m.items[m.jobItems["job-1"]].sys) }
	if !strings.Contains(row(), "correction pending · working") {
		t.Fatalf("row = %q", row())
	}
	step(events.JobCorrection{ID: "job-1", Revision: 1, Text: "use the other file", State: "included"})
	if !strings.Contains(row(), "correction included · working") {
		t.Fatalf("row = %q", row())
	}
	step(events.JobFinished{ID: "job-1", Task: "summarize the notes", Elapsed: 12 * time.Second, Summary: "Summarized the other file."})
	sealed := stripANSI(m.items[m.completedJobItems["job-1"]].sys)
	if !strings.Contains(sealed, "correction included · done in 12s") {
		t.Fatalf("sealed row = %q", sealed)
	}
}

func TestWorkersCorrectKeyStartsTheCommand(t *testing.T) {
	controller := &demoWorkerController{items: []pipeline.WorkerInfo{{ID: "job-1", Task: "summarize the notes", State: "running", Correction: "correction included"}}}
	m := newWorkers(&config.Config{}, controller)
	m.width, m.height, m.cursor = 100, 24, 2
	m.returnScreen = screenConversation
	if view := stripANSI(m.View()); !strings.Contains(view, "correction included") || !strings.Contains(view, "e correct") {
		t.Fatalf("view = %q", view)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if cmd == nil {
		t.Fatal("no command for e")
	}
	if msg, ok := cmd().(tellWorkerMsg); !ok || msg.jobID != "job-1" {
		t.Fatalf("msg = %#v", msg)
	}
	m.returnScreen = screenLauncher
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if cmd != nil || !strings.Contains(m.message, "/work tell job-1") {
		t.Fatalf("outside a conversation: cmd=%v message=%q", cmd != nil, m.message)
	}

	conv := sizedConversation(t, 100, 24)
	conv.prefillCommand("/work tell job-1 ")
	if conv.input.Value() != "/work tell job-1 " {
		t.Fatalf("composer = %q", conv.input.Value())
	}
}

func TestJobRowFollowsTheNewestCorrection(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	step := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }
	row := func() string { return stripANSI(m.items[m.jobItems["job-1"]].sys) }
	step(events.JobStarted{ID: "job-1", Task: "summarize the notes"})
	step(events.JobCorrection{ID: "job-1", Revision: 1, State: "pending"})
	step(events.JobCorrection{ID: "job-1", Revision: 2, State: "pending"})
	step(events.JobCorrection{ID: "job-1", Revision: 1, State: "included"})
	if !strings.Contains(row(), "correction pending") {
		t.Fatalf("an older correction's disposition replaced the newest: %q", row())
	}
	step(events.JobCorrection{ID: "job-1", Revision: 2, State: "included"})
	if !strings.Contains(row(), "correction included") {
		t.Fatalf("row = %q", row())
	}
}

func TestInboxResultRowKeepsTheCorrection(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m, _ = m.Update(busEventMsg{event: events.JobCorrection{ID: "weather", Revision: 1, State: "included"}})
	m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
	idx, ok := m.workResultItems["forecast"]
	if !ok {
		t.Fatal("no result row")
	}
	if row := stripANSI(m.items[idx].sys); !strings.Contains(row, "correction included") {
		t.Fatalf("result row = %q", row)
	}
}

func TestWorkTellIsACommandThroughSubmitAndPaste(t *testing.T) {
	for _, line := range []string{"/work tell job-1 use chart.png", "/work tell job-1 use chart.png"} {
		m := sizedConversation(t, 120, 24)
		runner := &tellRunner{fakeTurnRunner: &fakeTurnRunner{}, receipt: pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: "job-1", Outcome: pipeline.ControlAccepted, Revision: 1}}}}
		m.deps.runner = runner
		m.input.SetValue(line)
		m.handleSubmit()
		if len(runner.texts) != 1 || runner.texts[0] != "use chart.png" || len(m.media.draft) != 0 || m.media.importing {
			t.Fatalf("%q: sent %q, draft=%d importing=%v", line, runner.texts, len(m.media.draft), m.media.importing)
		}
	}
	m := sizedConversation(t, 120, 24)
	m, cmd := m.Update(clipboardPasteMsg{text: "/work tell job-1 use chart.png"})
	if m.media.importing || cmd != nil && len(m.media.draft) != 0 {
		t.Fatal("a pasted /work tell line was taken for an image path")
	}
	if !strings.Contains(m.input.Value(), "/work tell job-1 use chart.png") {
		t.Fatalf("composer = %q", m.input.Value())
	}
}

func TestWorkersCorrectKeyKeepsADraft(t *testing.T) {
	conv := sizedConversation(t, 100, 24)
	conv.input.SetValue("half a thought")
	conv.prefillCommand("/work tell job-1 ")
	if conv.input.Value() != "half a thought" {
		t.Fatalf("draft replaced: %q", conv.input.Value())
	}
	if notice := conv.items[len(conv.items)-1].notice; !strings.Contains(notice, "/work tell job-1") {
		t.Fatalf("notice = %q", notice)
	}
}

func TestWorkerThreadMarksAReplacedAnswer(t *testing.T) {
	item := pipeline.WorkerInfo{ID: "job-1", Task: "summarize the notes", State: "done", Thread: []brain.Turn{
		{Role: "user", Content: "summarize the notes"},
		{Role: "assistant", Content: "The notes say one thing.", Disposition: brain.DispositionSuperseded},
		{Role: "user", Content: "use the other file"},
		{Role: "assistant", Content: "The other file says another."},
	}}
	view := stripANSI(formatWorkerDetail(item, 100, false))
	if !strings.Contains(view, "replaced by a correction, not delivered") || strings.Count(view, "not delivered") != 1 {
		t.Fatalf("view = %q", view)
	}
}
