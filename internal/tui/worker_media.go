package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
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
	return tea.Batch(cmd, m.syncGraphics())
}
func (m *workersModel) updateWorkerMedia(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case mediaPlaced:
		if v.token != m.media.token {
			return nil, true
		}
		m.media.placed = v.placed
		m.refreshDetail()
		return nil, true
	case mediaLoaded:
		c := m.mediaConversation()
		cmd, ok := c.updateMedia(msg)
		m.media = c.media
		m.refreshDetail()
		return cmd, ok
	case mediaTick:
		c := m.mediaConversation()
		cmd, ok := c.updateMedia(msg)
		m.media = c.media
		m.refreshDetail()
		return tea.Batch(cmd, m.syncGraphics()), ok
	}
	return nil, false
}

// A fresh worker screen must not treat "none wanted" as already in effect:
// the terminal may still hold images an earlier one showed.
func (m *workersModel) adoptGraphics(g *mediaGraphics) {
	m.graphics = g
	if g != nil {
		m.media.wantSignature = "stale"
	}
}

// previewSize is the box a worker picture is drawn in. Drawing and syncing
// both use it, or the placed image is never found.
func (m workersModel) previewSize(attachments int) (int, int) {
	return max(1, m.detail.Width), previewRows(m.graphics != nil, m.detail.Height, attachments)
}

func (m workersModel) renderPreview(refs []chatmedia.Ref) string {
	w, h := m.previewSize(len(refs))
	return conversationModel{media: m.media, graphics: m.graphics}.renderMedia(refs, w, h)
}

func (m workersModel) graphicsPlacements() []mediaPlacement {
	var places []mediaPlacement
	for _, item := range m.items {
		if m.detailID == "" || item.ID != m.detailID {
			continue
		}
		for _, entry := range workerThreadEntries(item) {
			if len(entry.attachments) == 0 {
				continue
			}
			w, h := m.previewSize(len(entry.attachments))
			for _, r := range entry.attachments {
				places = append(places, mediaPlacement{r, w, h})
			}
		}
	}
	return places
}

func (m *workersModel) releaseGraphics() tea.Cmd {
	if m.graphics == nil {
		return nil
	}
	return m.graphics.syncCommand(mediaOwnerWorkers, &m.media, nil, nil)
}

// syncGraphics asks for the open detail's pictures; with no detail open it
// frees the worker view's images.
func (m *workersModel) syncGraphics() tea.Cmd {
	if m.graphics == nil {
		return nil
	}
	keep := make(map[string]bool, len(m.media.cache))
	for id := range m.media.cache {
		keep[id] = true
	}
	return m.graphics.syncCommand(mediaOwnerWorkers, &m.media, m.graphicsPlacements(), keep)
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
