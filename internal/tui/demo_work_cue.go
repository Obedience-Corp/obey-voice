package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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
	case "1", "true", "yes", "on", demoWorkCueCutShort:
		return true
	default:
		return false
	}
}

// demoWorkCueCutShort scripts a job that repeats one tool call until it is
// made to answer, and finishes cut short.
const demoWorkCueCutShort = "cut_short"

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
	if demoWorkCueVariant() == demoWorkCueCutShort {
		runDemoCutShort(emit)
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
