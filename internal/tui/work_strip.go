package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type workDisplayState struct {
	snapshot      work.Snapshot
	seen          map[string]bool
	observedAt    map[string]time.Time
	terminalUntil time.Time
	lastTerminal  string
}

type workStripExpiryMsg struct {
	scope    work.WorkScope
	deadline time.Time
}

func (m *conversationModel) observeWorkDisplay(s work.Snapshot) {
	d := &m.workDisplay
	if d.snapshot.Scope != s.Scope {
		*d = workDisplayState{}
	}
	if s.Revision < d.snapshot.Revision {
		return
	}
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if d.observedAt == nil {
		d.observedAt = map[string]time.Time{}
	}
	known := map[string]bool{}
	for _, r := range d.snapshot.Communicated {
		known[r.Outcome.ResultID] = true
	}
	for _, r := range s.Communicated {
		if !known[r.Outcome.ResultID] {
			d.lastTerminal = r.Task
			d.terminalUntil = time.Now().Add(5 * time.Second)
		}
	}
	d.snapshot = s
	retainedJobs, retainedResults := map[string]bool{}, map[string]bool{}
	for _, j := range s.Jobs {
		retainedJobs[j.Key.JobID] = true
		if d.observedAt[j.Key.JobID].IsZero() {
			d.observedAt[j.Key.JobID] = time.Now()
		}
	}
	for _, rows := range [][]work.ResultView{s.Pending, s.Communicated} {
		for _, r := range rows {
			retainedResults[r.Outcome.ResultID] = true
		}
	}
	for id := range d.seen {
		if !retainedResults[id] {
			delete(d.seen, id)
		}
	}
	for id := range d.observedAt {
		if !retainedJobs[id] {
			delete(d.observedAt, id)
		}
	}
}

type workCounts struct{ running, queued, updates, unseen, held, responding int }

func (m conversationModel) workCounts() workCounts {
	var c workCounts
	for _, j := range m.workDisplay.snapshot.Jobs {
		if j.State == work.JobRunning {
			c.running++
		}
		if j.State == work.JobQueued {
			c.queued++
		}
	}
	seen := map[string]bool{}
	for _, rows := range [][]work.ResultView{m.workDisplay.snapshot.Pending, m.workDisplay.snapshot.Communicated} {
		for _, r := range rows {
			id := r.Outcome.ResultID
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if !m.workDisplay.seen[id] {
				c.unseen++
			}
			if r.State != work.Communicated && r.State != work.Dismissed {
				c.updates++
			}
			if r.State == work.Held {
				c.held++
			}
			if r.AnnouncementState == work.AnnouncementScheduled {
				c.responding++
			}
		}
	}
	return c
}
func (m conversationModel) workStripRows(now time.Time) int {
	c := m.workCounts()
	if c.running+c.queued+c.updates+c.unseen == 0 && !now.Before(m.workDisplay.terminalUntil) {
		return 0
	}
	if m.width >= 80 && m.height >= 20 {
		return 3
	}
	if m.width >= 60 && m.height >= 20 {
		return 2
	}
	return 1
}

func (m *conversationModel) scheduleWorkStripExpiry() tea.Cmd {
	deadline := m.workDisplay.terminalUntil
	scope := m.workDisplay.snapshot.Scope
	if deadline.IsZero() {
		return nil
	}
	if deadline.Equal(m.workStripExpiryDeadline) && scope == m.workStripExpiryScope {
		return nil
	}
	m.workStripExpiryDeadline = deadline
	m.workStripExpiryScope = scope
	ctx := m.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		delay := time.Until(deadline)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil
			}
		}
		return workStripExpiryMsg{scope: scope, deadline: deadline}
	}
}

func (m conversationModel) renderWorkStrip(now time.Time) string {
	rows := m.workStripRows(now)
	if rows == 0 {
		return ""
	}
	c := m.workCounts()
	counts := fmt.Sprintf("Work %d running · %d queued · %d updates · %d new", c.running, c.queued, c.updates, c.unseen)
	if m.width < 60 {
		counts = fmt.Sprintf("Work %dr %dq %du %dn", c.running, c.queued, c.updates, c.unseen)
	}
	if c.held > 0 {
		counts += fmt.Sprintf(" · %d held", c.held)
	}
	if c.responding > 0 {
		counts += fmt.Sprintf(" · %d responding", c.responding)
	}
	lines := []string{counts}
	if rows > 1 {
		detail := "Result available"
		if c.held > 0 {
			detail = "Update held; /work shows the reason"
		} else if c.responding > 0 {
			detail = "Result response in progress"
		} else if c.updates > 0 {
			detail = "Result ready; waiting for a safe conversation boundary"
		} else if m.workDisplay.lastTerminal != "" {
			detail = "Completed · " + m.workDisplay.lastTerminal
		}
		if j := m.selectedWorkJob(); j != nil && (j.State == work.JobRunning || j.State == work.JobQueued) {
			phase := string(j.State)
			elapsed := ""
			if at := m.workDisplay.observedAt[j.Key.JobID]; !at.IsZero() {
				elapsed = " · observed " + roundSeconds(max(0, now.Sub(at)))
			}
			if controller, ok := m.deps.runner.(workerController); ok {
				for _, info := range controller.Workers() {
					if info.ID == j.Key.JobID && !info.Started.IsZero() {
						elapsed = " · " + roundSeconds(info.Elapsed)
						break
					}
				}
			}
			detail = "• " + j.Task + " · " + workTierLabel(j.Config["worker_tier"]) + " · " + phase + elapsed
		}
		lines = append(lines, detail)
	}
	if rows > 2 {
		lines = append(lines, "/work details · /workers execution controls · Ctrl+T Activity")
	}
	for i := range lines {
		lines[i] = ansi.Truncate(strings.Join(strings.Fields(lines[i]), " "), max(m.width, 1), "…")
	}
	return strings.Join(lines, "\n") + "\n"
}
func workTierLabel(tier string) string {
	switch tier {
	case "simple":
		return "Simple"
	case "medium":
		return "Medium"
	case "complex":
		return "Complex"
	default:
		return "Legacy"
	}
}
func (m conversationModel) selectedWorkJob() *work.JobView {
	jobs := append([]work.JobView(nil), m.workDisplay.snapshot.Jobs...)
	if m.workFilter != "" {
		for i := range jobs {
			if jobs[i].Key.JobID == m.workFilter {
				return &jobs[i]
			}
		}
	}
	rank := func(j work.JobView) int {
		switch j.State {
		case work.JobRunning:
			return 0
		case work.JobQueued:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if rank(jobs[i]) != rank(jobs[j]) {
			return rank(jobs[i]) < rank(jobs[j])
		}
		a, b := m.workDisplay.observedAt[jobs[i].Key.JobID], m.workDisplay.observedAt[jobs[j].Key.JobID]
		if !a.Equal(b) {
			return a.Before(b)
		}
		return jobs[i].Key.JobID < jobs[j].Key.JobID
	})
	if len(jobs) > 0 {
		return &jobs[0]
	}
	return nil
}
func (m *conversationModel) markVisibleWorkResults() {
	if m.activityFocused || m.workDisplayHidden || !m.ready {
		return
	}
	if m.workDisplay.seen == nil {
		m.workDisplay.seen = map[string]bool{}
	}
	start, end, line := m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height, 0
	for _, item := range m.items {
		rendered := item.render(m.showThink, m.viewport.Width)
		if rendered == "" {
			continue
		}
		h := lipgloss.Height(lipgloss.NewStyle().Width(max(m.viewport.Width, 1)).Render(rendered))
		if item.workResultID != "" && line < end && line+h > start {
			m.workDisplay.seen[item.workResultID] = true
		}
		line += h
	}
}
