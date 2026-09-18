package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const demoCaptureEnv = "OBEY_VOICE_DEMO_CAPTURE"

func demoCaptureEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoCaptureEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startDemoCapture(bus *events.Bus) tea.Cmd {
	if bus == nil {
		return nil
	}
	return func() tea.Msg {
		go runDemoCapture(bus)
		return demoThinkStartedMsg{}
	}
}

func runDemoCapture(bus *events.Bus) {
	time.Sleep(6 * time.Second)
	bus.Emit(events.Error{
		Stage:   "admit",
		Message: "admit response: persona name \"veronica\" is ambiguous between veronica, veronica-hype",
	})
}
