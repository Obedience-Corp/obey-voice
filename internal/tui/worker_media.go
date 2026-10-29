package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

// Reuse the conversation's bounded asynchronous preview loader and animation
// clock; the worker owns only its selected thread's media state.
func (m *workersModel) mediaConversation() conversationModel {
	c := conversationModel{media: m.media}
	for _, item := range m.items {
		if item.ID != m.detailID {
			continue
		}
		for _, turn := range item.Thread {
			c.items = append(c.items, chatItem{attachments: turn.Attachments})
		}
	}
	return c
}
func (m *workersModel) loadWorkerMedia() tea.Cmd {
	c := m.mediaConversation()
	cmd := c.mediaCommand()
	m.media = c.media
	return cmd
}
func (m *workersModel) updateWorkerMedia(msg tea.Msg) (tea.Cmd, bool) {
	switch msg.(type) {
	case mediaLoaded, mediaTick:
		c := m.mediaConversation()
		cmd, ok := c.updateMedia(msg)
		m.media = c.media
		m.refreshDetail()
		return cmd, ok
	}
	return nil, false
}

func desktopWorkerThreads(workers []pipeline.WorkerInfo) map[string][]DesktopTurn {
	out := make(map[string][]DesktopTurn)
	for _, worker := range workers {
		for _, turn := range worker.Thread {
			if turn.Role != "user" && turn.Role != "assistant" {
				continue
			}
			out[worker.ID] = append(out[worker.ID], DesktopTurn{Role: turn.Role, Content: turn.Content, Attachments: desktopMediaRefs(turn.Attachments)})
		}
	}
	return out
}
