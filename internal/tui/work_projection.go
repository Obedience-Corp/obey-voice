package tui

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// workStateChanged projects the coordinator's canonical pending results into
// the conversation thread. This is display state only: it does not claim,
// speak, acknowledge, or otherwise consume a result.
func (m *conversationModel) workStateChanged(e events.WorkStateChanged) {
	s := e.Snapshot
	if !s.Scope.Valid() {
		return
	}
	var authoritative work.WorkScope
	if m.deps.workScope != nil {
		authoritative = m.deps.workScope()
	}
	if authoritative.Valid() && authoritative != s.Scope {
		return
	}
	if m.workScope.Valid() && s.Scope == m.workScope && s.Revision < m.workRevision {
		return
	}
	if m.workScope.Valid() && m.workScope != s.Scope {
		m.clearWorkProjection()
		m.clearJobProjection()
	}
	m.workScope = s.Scope
	m.workRevision = s.Revision
	if m.workResultItems == nil {
		m.workResultItems = make(map[string]int)
	}
	if m.workResultJobs == nil {
		m.workResultJobs = make(map[string]string)
	}

	seen := make(map[string]struct{}, len(s.Pending))
	for _, result := range s.Pending {
		id := result.Outcome.ResultID
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
		line := renderWorkResult(result)
		if idx, ok := m.workResultItems[id]; ok && idx < len(m.items) {
			m.items[idx].sys = line
			continue
		}
		// Replace the provisional JobFinished line when lifecycle and canonical
		// state arrive in either order. This keeps one visible result per job.
		idx, ok := m.jobItems[result.Outcome.JobID]
		if !ok {
			idx, ok = m.completedJobItems[result.Outcome.JobID]
		}
		if ok && idx < len(m.items) {
			m.items[idx] = chatItem{kind: chatItemSys, sys: line, workResultID: id}
			delete(m.jobItems, result.Outcome.JobID)
			delete(m.completedJobItems, result.Outcome.JobID)
			m.workResultItems[id] = idx
		} else {
			m.workResultItems[id] = len(m.items)
			m.items = append(m.items, chatItem{kind: chatItemSys, sys: line, workResultID: id})
		}
		m.workResultJobs[result.Outcome.JobID] = id
	}
	for id, idx := range m.workResultItems {
		if _, ok := seen[id]; ok {
			continue
		}
		if idx < len(m.items) && m.items[idx].workResultID == id {
			m.items[idx].sys = ""
			m.items[idx].workResultID = ""
		}
		delete(m.workResultItems, id)
	}
	m.refreshContent()
	m.applyFollow()
}

func renderWorkResult(v work.ResultView) string {
	text := strings.TrimSpace(v.Outcome.Text)
	if text == "" {
		text = string(v.Outcome.Status)
	}
	prefix := "  Background result"
	if v.Outcome.PreviousSession {
		prefix = "  Previous session update"
	} else if v.State == work.Held {
		prefix = "  Background result (held)"
	}
	return dimStyle.Render(prefix + ": " + text)
}

func (m *conversationModel) clearWorkProjection() {
	for id, idx := range m.workResultItems {
		if idx < len(m.items) && m.items[idx].workResultID == id {
			m.items[idx].sys = ""
			m.items[idx].workResultID = ""
		}
	}
	m.workResultItems = make(map[string]int)
	m.workResultJobs = make(map[string]string)
}

func (m *conversationModel) clearJobProjection() {
	for id, idx := range m.jobItems {
		if idx < len(m.items) {
			m.items[idx].sys = ""
		}
		delete(m.jobItems, id)
	}
	m.jobs = make(map[string]*jobView)
	m.jobOrder = nil
	m.completedJobItems = make(map[string]int)
	m.work = workfold.New()
}

func (m *conversationModel) hasCanonicalJob(id string) bool {
	_, ok := m.workResultJobs[id]
	return ok
}
