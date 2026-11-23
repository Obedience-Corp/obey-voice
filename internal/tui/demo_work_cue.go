package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// demoWorkCueEnv scripts a handoff and a second conversation turn while the
// workers-screen fixture remains active. VHS records the real conversation
// renderer and worker detail without a microphone, model, or external tools.
const demoWorkCueEnv = "OBEY_VOICE_DEMO_WORK_CUE"
const demoWorkCueTask = "Check the weather in Denver, Colorado"

type demoWorkCueStartedMsg struct{}

func demoWorkCueVariant() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv(demoWorkCueEnv)))
}

func demoWorkCueEnabled() bool {
	switch demoWorkCueVariant() {
	case "1", "true", "yes", "on", demoWorkCueCutShort, demoWorkCueTell:
		return true
	default:
		return false
	}
}

// demoWorkCueCutShort scripts a job that repeats one tool call until it is
// made to answer, and finishes cut short.
const demoWorkCueCutShort = "cut_short"

// demoWorkCueTell scripts a running job that the user corrects with
// /work tell; demoTellRunner answers the command and scripts the worker's side.
const demoWorkCueTell = "tell"

const demoTellTask = "Summarize the action items in notes.txt"

func startDemoWorkCue(bus *events.Bus) tea.Cmd {
	if bus == nil {
		return nil
	}
	return func() tea.Msg {
		go runDemoWorkCue(bus)
		return demoWorkCueStartedMsg{}
	}
}

func runDemoWorkCue(bus *events.Bus) {
	emit := func(e events.Event) { bus.Emit(e) }
	switch demoWorkCueVariant() {
	case demoWorkCueCutShort:
		runDemoCutShort(emit)
		return
	case demoWorkCueTell:
		runDemoTell(emit)
		return
	}
	time.Sleep(600 * time.Millisecond)
	emit(events.UserInput{Text: "Jarvis, check the weather in Denver."})
	emit(events.ThinkingStarted{})
	time.Sleep(700 * time.Millisecond)
	emit(events.JobStarted{ID: "job-1", Task: demoWorkCueTask})
	emit(events.ResponseReady{Response: "Working on it, sir."})
	emit(events.STTPhase{Phase: "listening"})

	// Leave the first turn on screen long enough to see the two independent
	// animations. A follow-up then proves the worker stays active behind chat.
	time.Sleep(6 * time.Second)
	emit(events.UserInput{Text: "Can I keep talking while you work?"})
	emit(events.ThinkingStarted{})
	time.Sleep(850 * time.Millisecond)
	emit(events.ResponseReady{Response: "Of course. The background task is still running."})
	emit(events.STTPhase{Phase: "listening"})
}

func runDemoCutShort(emit func(events.Event)) {
	const task = "Check the build log for the failing step"
	time.Sleep(600 * time.Millisecond)
	emit(events.UserInput{Text: "Check the build log for the failing step."})
	emit(events.ThinkingStarted{})
	time.Sleep(700 * time.Millisecond)
	emit(events.JobStarted{ID: "job-1", Task: task})
	emit(events.ResponseReady{Response: "Working on it."})
	emit(events.STTPhase{Phase: "listening"})
	ok := &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}
	for i := range 3 {
		id := "call-" + string(rune('1'+i))
		time.Sleep(900 * time.Millisecond)
		emit(events.ToolCallStarted{ID: id, JobID: "job-1", Name: "read_file", Kind: "read"})
		time.Sleep(500 * time.Millisecond)
		emit(events.ToolCallFinished{ID: id, JobID: "job-1", Name: "read_file", Kind: "read", Outcome: ok})
	}
	time.Sleep(1200 * time.Millisecond)
	emit(events.JobFinished{ID: "job-1", Task: task, Elapsed: 41 * time.Second, CutShort: true, CutShortReason: "repeated the same tool call",
		Summary: "I could not get past reading build.log: it kept returning the same lines. The failing step is not identified yet."})
}

func runDemoTell(emit func(events.Event)) {
	time.Sleep(600 * time.Millisecond)
	emit(events.UserInput{Text: "Summarize the action items in notes.txt."})
	emit(events.ThinkingStarted{})
	time.Sleep(700 * time.Millisecond)
	emit(events.JobStarted{ID: "job-1", Task: demoTellTask})
	emit(events.ResponseReady{Response: "Working on it."})
	emit(events.STTPhase{Phase: "listening"})
	ok := &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}
	time.Sleep(900 * time.Millisecond)
	emit(events.ToolCallStarted{ID: "call-1", JobID: "job-1", Name: "read_file", Kind: "read", Summary: "notes.txt"})
	time.Sleep(500 * time.Millisecond)
	emit(events.ToolCallFinished{ID: "call-1", JobID: "job-1", Name: "read_file", Kind: "read", Outcome: ok})
}

// demoTellRunner is the live pipeline with /work tell answered by a script:
// the correction is accepted at once, as on an in-process worker, and the
// worker's next request, its reread and its answer follow.
type demoTellRunner struct {
	*pipeline.Pipeline
	bus *events.Bus
}

func (d demoTellRunner) TellWork(id, text string) pipeline.WorkReceipt {
	d.bus.Emit(events.JobCorrection{ID: id, Revision: 1, Text: text, State: "pending"})
	go func() {
		ok := &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}
		time.Sleep(1500 * time.Millisecond)
		d.bus.Emit(events.JobCorrection{ID: id, Revision: 1, Text: text, State: "included"})
		time.Sleep(600 * time.Millisecond)
		d.bus.Emit(events.ToolCallStarted{ID: "call-2", JobID: id, Name: "read_file", Kind: "read", Summary: "notes-2.txt"})
		time.Sleep(500 * time.Millisecond)
		d.bus.Emit(events.ToolCallFinished{ID: "call-2", JobID: id, Name: "read_file", Kind: "read", Outcome: ok})
		time.Sleep(1200 * time.Millisecond)
		d.bus.Emit(events.JobFinished{ID: id, Task: demoTellTask, Elapsed: 18 * time.Second,
			Summary: "notes-2.txt has three action items: send the draft, book the room, and update the budget sheet."})
	}()
	return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: pipeline.ControlAccepted, Revision: 1}}}
}
