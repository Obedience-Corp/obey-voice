package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// demoThinkEnv scripts a thinking turn for VHS recordings. Real mic/TTS/brain
// are not required; the same bus events production turns emit drive the TUI.
const demoThinkEnv = "OBEY_VOICE_DEMO_THINK"

type demoThinkStartedMsg struct{}

func demoThinkEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoThinkEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startDemoThink(bus *events.Bus) tea.Cmd {
	if bus == nil {
		return nil
	}
	return func() tea.Msg {
		go runDemoThink(bus)
		return demoThinkStartedMsg{}
	}
}

func runDemoThink(bus *events.Bus) {
	sleep := func(d time.Duration) { time.Sleep(d) }
	emit := func(e events.Event) { bus.Emit(e) }

	sleep(500 * time.Millisecond)
	emit(events.UserInput{Text: "Why is the sky blue?"})
	sleep(400 * time.Millisecond)

	emit(events.ThinkingStarted{})
	sleep(350 * time.Millisecond)

	thinkChunks := []string{
		"The user asked why the sky is blue. ",
		"This is Rayleigh scattering: shorter wavelengths ",
		"scatter more in air than longer ones. ",
		"Blue is shorter than red, so the daytime sky looks blue.",
	}
	for _, chunk := range thinkChunks {
		emit(events.ThinkingDelta{Text: chunk})
		sleep(450 * time.Millisecond)
	}
	sleep(400 * time.Millisecond)

	emit(events.ResponseStreamingStarted{})
	spoken := "The sky looks blue because air scatters short wavelengths more — Rayleigh scattering."
	for _, word := range strings.Fields(spoken) {
		emit(events.ResponseDelta{Text: word + " "})
		sleep(90 * time.Millisecond)
	}
	sleep(300 * time.Millisecond)
	emit(events.ThinkingComplete{Elapsed: 2200 * time.Millisecond})
	emit(events.ResponseReady{Response: spoken})
	// Hold the finalized, speakable-only thread so the tape can press Ctrl+K.
	sleep(8 * time.Second)
}
