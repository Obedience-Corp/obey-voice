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

func demoWorkCueEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoWorkCueEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

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
