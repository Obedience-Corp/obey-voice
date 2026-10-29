package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height
	m.reflow()
}

func (m *conversationModel) reflow() {
	m.reflowAt(time.Now())
}

func (m *conversationModel) reflowAt(now time.Time) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Capture follow intent before height changes invalidate AtBottom().
	m.syncFollowFromViewports()
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Header + rule + label + input border + footer consume six rows in
	// addition to the textarea's own height. An active voice panel adds a few
	// more. Command matches consume only the rows currently available above
	// the composer.
	m.workStripReservedRows = m.workStripRows(now)
	m.workStripRenderAt = now
	chrome := 6 + m.workStripReservedRows
	chrome += m.voicePanelHeight() + m.contextMeterHeight()
	vpHeight := max(m.height-inputHeight-chrome-m.commandPaletteRows(), 1)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpHeight)
		m.activityViewport = viewport.New(max(m.width, 1), vpHeight)
		m.ready = true
		m.followChat = true
		m.followActivity = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpHeight
		m.activityViewport.Width = max(m.width, 1)
		m.activityViewport.Height = vpHeight
	}
	m.input.SetWidth(max(m.width-4, 1))
	m.input.SetHeight(inputHeight)
	m.refreshContent()
	m.refreshActivity()
	m.applyFollow()
}

// syncFollowFromViewports updates the sticky follow flags from the current
// scroll position. Call before mutating content or viewport height so a
// reflow cannot flip "at bottom" into a permanent freeze.
func (m *conversationModel) syncFollowFromViewports() {
	if !m.ready {
		m.followChat = true
		m.followActivity = true
		return
	}
	m.followChat = m.viewport.AtBottom()
	m.followActivity = m.activityViewport.AtBottom()
}

// applyFollow pins each pane to the tail when its follow flag is set.
func (m *conversationModel) applyFollow() {
	if !m.ready {
		return
	}
	if m.followChat {
		m.viewport.GotoBottom()
	}
	if m.followActivity {
		m.activityViewport.GotoBottom()
	}
	m.markVisibleWorkResults()
}

func (m *conversationModel) activeViewport() *viewport.Model {
	if m.activityFocused {
		return &m.activityViewport
	}
	return &m.viewport
}

func (m conversationModel) updateScroll(msg tea.Msg) (conversationModel, tea.Cmd) {
	var cmd tea.Cmd
	if m.activityFocused {
		m.activityViewport, cmd = m.activityViewport.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	// Manual scroll owns follow: pgup freezes the tail; jumping to bottom
	// (End / ctrl+end) re-enables auto-follow for new messages.
	m.syncFollowFromViewports()
	m.markVisibleWorkResults()
	return m, cmd
}
