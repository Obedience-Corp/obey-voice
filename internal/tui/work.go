package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// Background work rendering. A worker's tool calls belong to the job, not to
// the conversation: they go to the activity feed and the footer chip, and never
// touch the status line or the orb — otherwise a job running behind a live turn
// hijacks both. State is kept per job: a spawn queued behind a running worker
// is counted on the chip but never relabels it. The thread gets exactly one
// item per job, rewritten in place as the fold grows, the same way the
// streaming think block is rewritten.

// jobView is what the chip and the job's thread item know about one queued
// or running job.
type jobView struct {
	task     string
	origin   string
	running  bool
	terminal bool
	tool     string // the tool the worker is on, until the fold has a row
	tier     string
}

func (m *conversationModel) canonicalJob(v work.JobView) {
	id := v.Key.JobID
	if id == "" {
		return
	}
	job := m.trackJob(id, v.Task)
	job.terminal = v.State == work.JobTerminal
	job.origin = v.Origin
	if v.Config != nil {
		job.tier = v.Config["worker_tier"]
	}
	job.running = v.State == work.JobRunning
	if v.State == work.JobQueued {
		m.renderJobItem(id, " · queued")
	} else if v.State == work.JobRunning {
		m.renderJobItem(id, " · working")
	}
	m.refreshJobChip()
}

func (m *conversationModel) jobQueued(e events.JobQueued) {
	m.trackJob(e.ID, e.Task)
	m.appendActivity("job", "queued "+e.Task, 0)
	m.refreshJobChip()
	m.renderJobItem(e.ID, " · queued")
}

func (m *conversationModel) routeDecided(e events.RouteDecided) {
	if !m.acceptWorkScope(e.Scope) {
		return
	}
	if e.State == "rejected" {
		msg := e.Message
		if msg == "" {
			msg = "routing rejected"
		}
		m.setStatus("Worker rejected: "+msg, true)
		m.appendTranscript(dimStyle.Render("  Worker rejected · " + msg))
		return
	}
	if e.State != "accepted" || e.JobID == "" {
		return
	}
	v := m.trackJob(e.JobID, e.Task)
	v.tier = e.Tier
	m.appendActivity("job", "handed off · "+e.Tier+" · "+e.Task, 0)
	m.renderJobItem(e.JobID, " · handed off · "+e.Tier)
	m.setStatus("Handed off · "+e.Tier+" · "+e.Task, false)
}

func (m *conversationModel) jobStarted(e events.JobStarted) {
	m.trackJob(e.ID, e.Task).running = true
	m.appendActivity("job", "started "+e.Task, 0)
	m.refreshJobChip()
	m.renderJobItem(e.ID, " · working")
}

func (m *conversationModel) jobFinished(e events.JobFinished) {
	if !m.acceptWorkScope(e.Scope) {
		return
	}
	if m.workerRepliesSeen[e.ID] {
		return
	}
	if m.workerRepliesSeen == nil {
		m.workerRepliesSeen = make(map[string]bool)
	}
	m.workerRepliesSeen[e.ID] = true
	m.jobEnded(e.ID, "finished", " · done in "+roundSeconds(e.Elapsed), e.Elapsed)
	if text := strings.TrimSpace(e.Summary); text != "" {
		name := m.agentName
		if e.Envelope != nil && e.Envelope.DisplayName != "" {
			name = e.Envelope.DisplayName
		}
		m.rememberAssistant(text)
		m.appendAgentTurn(chatItem{name: name, spoken: text})
	}
}

func (m *conversationModel) jobFailed(e events.JobFailed) {
	detail := e.Err
	if detail == "" {
		detail = "failed"
	}
	m.jobEnded(e.ID, "failed: "+detail, " · failed: "+detail, e.Elapsed)
}

func (m *conversationModel) jobAborted(e events.JobAborted) {
	stopped := "killed after " + roundSeconds(e.Elapsed)
	if e.Reason == events.AbortBudget {
		stopped = "over budget after " + roundSeconds(e.Elapsed)
	}
	m.jobEnded(e.ID, stopped, " · "+stopped, 0)
}

// jobEnded logs the transition, seals the job's item with its outcome, and
// forgets the job before the chip is recomputed for whatever is left.
func (m *conversationModel) jobEnded(id, detail, suffix string, elapsed time.Duration) {
	m.appendActivity("job", detail, elapsed)
	// Reconcile missing end hooks before sealing the line. Unknown calls must
	// remain visible and cannot be rendered as successful work.
	rows := m.workTracker().Close(id)
	unknown := 0
	for _, row := range rows {
		unknown += row.Unknown
	}
	if unknown > 0 {
		m.appendActivity("job", fmt.Sprintf("%d tool outcome(s) unknown", unknown), 0)
	}
	label := workfold.RowsSummary(rows)
	if label == "" {
		label = m.jobLabel(id)
	}
	if !m.hasCanonicalJob(id) {
		m.renderJobItemLabel(id, label, suffix)
		if idx, ok := m.jobItems[id]; ok {
			if m.completedJobItems == nil {
				m.completedJobItems = make(map[string]int)
			}
			m.completedJobItems[id] = idx
			if len(m.completedJobItems) > 256 {
				for old := range m.completedJobItems {
					delete(m.completedJobItems, old)
					break
				}
			}
		}
	}
	delete(m.jobItems, id)
	m.forgetJob(id)
	m.refreshJobChip()
}

func (m *conversationModel) jobToolStarted(e events.ToolCallStarted) {
	if !m.acceptWorkScope(e.WorkScope) {
		return
	}
	m.workTracker().Add(e)
	m.noteJobTool(e.JobID, e.Name)
	m.appendActivity("job", jobToolDetail(e.Name, e.Summary), 0)
	m.renderJobItem(e.JobID, " · working")
}

func (m *conversationModel) jobToolFinished(e events.ToolCallFinished) {
	if !m.acceptWorkScope(e.WorkScope) {
		return
	}
	if _, ok := m.jobs[e.JobID]; !ok {
		return
	}
	m.workTracker().Finish(e)
	m.noteJobTool(e.JobID, e.Name)
	m.appendActivity("job", toolOutcomeDetail(e), e.ResultElapsed())
	m.renderJobItem(e.JobID, " · working")
}

func (m *conversationModel) acceptWorkScope(scope work.WorkScope) bool {
	if !scope.Valid() {
		return true
	}
	if m.deps.workScope == nil {
		return false
	}
	authoritative := m.deps.workScope()
	return authoritative.Valid() && authoritative == scope
}

func toolOutcomeLabel(status events.ToolOutcomeStatus) string {
	switch status {
	case events.ToolOutcomeSucceeded:
		return "completed"
	case events.ToolOutcomeFailed:
		return "failed"
	case events.ToolOutcomeTimedOut:
		return "timed out"
	case events.ToolOutcomeCanceled:
		return "canceled"
	default:
		return "outcome unknown"
	}
}

func toolOutcomeDetail(e events.ToolCallFinished) string {
	detail := e.Name + " " + toolOutcomeLabel(e.OutcomeStatus())
	if preview := e.ResultPreview(); preview != "" {
		detail += " → " + preview
	}
	if reason := e.ResultReason(); reason != "" {
		detail += ": " + reason
	}
	if e.Outcome != nil {
		if e.Outcome.ExitCode != nil {
			detail += fmt.Sprintf(" (exit code %d)", *e.Outcome.ExitCode)
		}
		if e.Outcome.Signal != "" {
			detail += " (signal " + e.Outcome.Signal + ")"
		}
	}
	if elapsed := e.ResultElapsed(); elapsed > 0 {
		detail += " in " + elapsed.String()
	}
	return detail
}

// trackJob returns the job's view, creating it on first sight so a stream
// joined mid-job still renders.
func (m *conversationModel) trackJob(id, task string) *jobView {
	if m.jobs == nil {
		m.jobs = make(map[string]*jobView)
	}
	v, ok := m.jobs[id]
	if !ok {
		v = &jobView{task: task}
		m.jobs[id] = v
		m.jobOrder = append(m.jobOrder, id)
	}
	return v
}

func (m *conversationModel) forgetJob(id string) {
	delete(m.jobs, id)
	for i, other := range m.jobOrder {
		if other == id {
			m.jobOrder = append(m.jobOrder[:i], m.jobOrder[i+1:]...)
			return
		}
	}
}

// noteJobTool records the tool a job is on. A tool event for a job never
// announced means the stream was joined mid-job; the id is all there is to
// name it by.
func (m *conversationModel) noteJobTool(id, name string) {
	v, ok := m.jobs[id]
	if !ok {
		v = m.trackJob(id, id)
		v.running = true
	}
	v.tool = name
	m.refreshJobChip()
}

// refreshJobChip counts only the jobs this conversation tracks, and must not
// reach for managed work: View prefers deps.workersSummary when it is set, and
// that is the source which sees the runtime's managed jobs.
func (m *conversationModel) refreshJobChip() {
	running, queued := m.jobCounts()
	m.jobStatus = ""
	if running+queued > 0 {
		m.jobStatus = workersCountLabel(running, queued, m.cfg == nil || !m.cfg.VoiceWorkersDisabled)
	}
}

func (m conversationModel) jobCounts() (running, queued int) {
	for _, v := range m.jobs {
		if v.terminal {
			continue
		}
		if v.running {
			running++
		} else {
			queued++
		}
	}
	return running, queued
}

// activeWorkerCounts reads the live table when available. A conversation can
// attach after a job started, before its lifecycle events reach this model.
func (m conversationModel) activeWorkerCounts() (running, queued int) {
	if m.deps.workers == nil {
		return m.jobCounts()
	}
	for _, item := range m.deps.workers() {
		if item.State == "queued" {
			queued++
		} else if item.Active() {
			running++
		}
	}
	return running, queued
}

// workerShortcutID opens the sole active worker, or the newest completed
// worker when none are active. Several active workers open the manager list.
func (m conversationModel) workerShortcutID() string {
	id := ""
	if m.deps.workers != nil {
		items := m.deps.workers()
		for _, item := range items {
			if item.State != "queued" && !item.Active() {
				continue
			}
			if id != "" {
				return ""
			}
			id = item.ID
		}
		if id == "" && len(items) > 0 {
			return items[0].ID
		}
		return id
	}
	for _, candidate := range m.jobOrder {
		job := m.jobs[candidate]
		if job == nil || job.terminal {
			continue
		}
		if id != "" {
			return ""
		}
		id = candidate
	}
	return id
}

// renderJobItem writes the job's single transcript item, appending it the first
// time and rewriting it in place afterwards. suffix seals the line at job end.
func (m *conversationModel) renderJobItem(id, suffix string) {
	if id == "" {
		return
	}
	m.renderJobItemLabel(id, m.jobLabel(id), suffix)
}

func (m *conversationModel) renderJobItemLabel(id, label, suffix string) {
	if id == "" {
		return
	}
	origin := ""
	if job := m.jobs[id]; job != nil && job.origin != "" {
		origin = " [" + job.origin + "]"
	}
	line := dimStyle.Render("  ⚙ " + id + origin + " · " + label + suffix)
	idx, ok := m.jobItems[id]
	if !ok || idx >= len(m.items) {
		if m.jobItems == nil {
			m.jobItems = make(map[string]int)
		}
		m.jobItems[id] = len(m.items)
		m.appendTranscript(line)
		return
	}
	m.items[idx] = chatItem{kind: chatItemSys, sys: line}
	m.refreshContent()
	m.applyFollow()
}

// jobLabel is what the job's line says it did: the folded rows once there are
// any, and the task until then.
func (m *conversationModel) jobLabel(id string) string {
	if summary := m.workTracker().Summary(id); summary != "" {
		return summary
	}
	if v := m.jobs[id]; v != nil {
		label := truncateJobLine(v.task)
		if v.tier != "" {
			label = v.tier + " · " + label
		}
		return label
	}
	return id
}

// workTracker lazily builds the fold so a zero-value model (tests, background
// helpers) still folds rather than panicking.
func (m *conversationModel) workTracker() *workfold.Tracker {
	if m.work == nil {
		m.work = workfold.New()
	}
	return m.work
}

func jobToolDetail(name, summary string) string {
	if summary == "" {
		return name
	}
	return name + " (" + summary + ")"
}

func roundSeconds(d time.Duration) string { return d.Round(time.Second).String() }
