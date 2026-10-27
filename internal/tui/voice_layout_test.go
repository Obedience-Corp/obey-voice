package tui

import (
	"fmt"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/charmbracelet/lipgloss"
)

func TestVoiceTransitionsKeepConversationInsideTerminal(t *testing.T) {
	for _, width := range []int{40, 80, 120, 200} {
		for _, height := range []int{14, 24, 50} {
			for _, reduced := range []bool{false, true} {
				t.Run(fmt.Sprintf("%dx%d-reduced%t", width, height, reduced), func(t *testing.T) {
					m := sizedConversation(t, width, height)
					m.reducedMotion = reduced
					m.handleEvent(events.JobStarted{ID: "one", Task: "Check files"})
					m.handleEvent(events.JobStarted{ID: "two", Task: "Check tests"})
					for _, mode := range []anim.Mode{anim.ModeListening, anim.ModeHearing, anim.ModeThinking, anim.ModeSynthesizing, anim.ModeSpeaking, anim.ModeListening, anim.ModeError, anim.ModeIdle} {
						m.setVoiceMode(mode)
						view := m.View()
						if got := lipgloss.Height(view); got != height {
							t.Errorf("mode %v: rendered %d rows into %d-row terminal", mode, got, height)
						}
					}
				})
			}
		}
	}
}
