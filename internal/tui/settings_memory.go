package tui

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func (m settingsModel) memoryRows() []settingRow {
	if m.cfg.BrainProvider != "ollama" {
		return nil
	}
	sizes := []int{8192, 16384, 32768}
	labels := []string{"8k", "16k", "32k"}
	current := -1
	value := fmt.Sprintf("Custom (%d / %d)", m.cfg.OllamaNumCtx, m.cfg.ContinuityNumCtxCeil)
	for i, size := range sizes {
		if m.cfg.OllamaNumCtx == size && m.cfg.ContinuityNumCtxCeil == size {
			current = i
			value = labels[i]
		}
	}
	return []settingRow{{key: "ollama_num_ctx", name: "Conversation memory", kind: rowChoice, value: value, options: labels, current: current, applies: appliesNextConversation,
		help: "How much the conversation remembers. Sets its window and ceiling together; workers keep their own window.",
		set: func(m *settingsModel, i int) tea.Cmd {
			if i < 0 || i >= len(sizes) {
				return nil
			}
			save := m.saveMemory
			if save == nil {
				save = config.SetConversationMemory
			}
			if err := save(sizes[i]); err != nil {
				m.message = "Conversation memory was not saved: " + err.Error()
				return nil
			}
			m.cfg.OllamaNumCtx = sizes[i]
			m.cfg.ContinuityNumCtxCeil = sizes[i]
			m.message = "Conversation memory saved; applies to the next conversation"
			return nil
		}}}
}
