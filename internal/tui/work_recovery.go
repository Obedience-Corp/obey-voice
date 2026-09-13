package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
	"time"
)

type workProjectionPollMsg struct{ generation uint64 }

// Diagnostics must recover even with animation disabled or voice input paused.
// This read-only timer is separate from the host's background-delivery gate.
func (m *conversationModel) workProjectionPollCmd() tea.Cmd {
	if m.deps.workSnapshot == nil || m.deps.ctx == nil {
		return nil
	}
	ctx, generation := m.deps.ctx, m.workPollGeneration
	return func() tea.Msg {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return workProjectionPollMsg{generation: generation}
		}
	}
}

func (m *conversationModel) recoverWork(now time.Time, force bool) {
	if m.deps.workSnapshot == nil || (!force && !m.lastWorkPoll.IsZero() && now.Sub(m.lastWorkPoll) < time.Second) {
		return
	}
	m.lastWorkPoll = now
	s := m.deps.workSnapshot()
	if !s.Scope.Valid() {
		return
	}
	if m.workScope.SessionID != s.Scope.SessionID || m.workScope.RuntimeEpoch != s.Scope.RuntimeEpoch {
		m.journalIncomplete = false
	}
	if s.Scope != m.workScope || s.Revision > m.workRevision {
		m.handleEvent(events.WorkStateChanged{Snapshot: s})
	}
	if m.deps.journalHealth != nil {
		h := m.deps.journalHealth()
		if h.Scope == s.Scope && h.Incomplete {
			m.journalIncomplete = true
		}
	}
}
