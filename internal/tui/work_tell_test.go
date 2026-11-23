package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
