package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func teaWindowSize(width, height int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: width, Height: height}
}

var transcriptBenchSizes = []int{100, 1000, 5000}

func benchTranscript(b *testing.B, run func(b *testing.B, m conversationModel)) {
	for _, n := range transcriptBenchSizes {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			truecolorTB(b)
			m := longConversation(b, n, 120, 40)
			b.ReportAllocs()
			b.ResetTimer()
			run(b, m)
		})
	}
}

func BenchmarkTranscriptWheel(b *testing.B) {
	benchTranscript(b, func(b *testing.B, m conversationModel) {
		for i := 0; b.Loop(); i++ {
			button := tea.MouseButtonWheelDown
			if i%2 == 1 {
				button = tea.MouseButtonWheelUp
			}
			m, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: button, Action: tea.MouseActionPress})
		}
	})
}

func BenchmarkTranscriptWheelAllSeen(b *testing.B) {
	benchTranscript(b, func(b *testing.B, m conversationModel) {
		for id := range m.workResultItems {
			m.workDisplay.seen[id] = true
		}
		for i := 0; b.Loop(); i++ {
			button := tea.MouseButtonWheelDown
			if i%2 == 1 {
				button = tea.MouseButtonWheelUp
			}
			m, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: button, Action: tea.MouseActionPress})
		}
	})
}

func BenchmarkTranscriptPageUp(b *testing.B) {
	benchTranscript(b, func(b *testing.B, m conversationModel) {
		for i := 0; b.Loop(); i++ {
			key := tea.KeyMsg{Type: tea.KeyPgDown}
			if i%2 == 1 {
				key = tea.KeyMsg{Type: tea.KeyPgUp}
			}
			m, _ = m.Update(key)
		}
	})
}
