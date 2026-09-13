package tui

import (
	"fmt"
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
}

func (m *conversationModel) canonicalJob(v work.JobView) {
	id := v.Key.JobID
	if id == "" {
		return
	}
	job := m.trackJob(id, v.Task)
	job.terminal = v.State == work.JobTerminal
	job.origin = v.Origin
	job.running = v.State == work.JobRunning
	if v.State == work.JobQueued {
		m.renderJobItem(id, " · queued")
	} else if v.State == work.JobRunning {
		m.renderJobItem(id, "")
	}
	m.refreshJobChip()
}

func (m *conversationModel) jobQueued(e events.JobQueued) {
	m.trackJob(e.ID, e.Task)
	m.appendActivity("job", "queued "+e.Task, 0)
	m.refreshJobChip()
	m.renderJobItem(e.ID, " · queued")
}

func (m *conversationModel) jobStarted(e events.JobStarted) {
	m.trackJob(e.ID, e.Task).running = true
	m.appendActivity("job", "started "+e.Task, 0)
	m.refreshJobChip()
	m.renderJobItem(e.ID, "")
}

func (m *conversationModel) jobFinished(e events.JobFinished) {
	m.jobEnded(e.ID, "finished", " · done in "+roundSeconds(e.Elapsed), e.Elapsed)
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
	m.renderJobItem(e.JobID, "")
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
	m.renderJobItem(e.JobID, "")
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

// refreshJobChip keeps prose in the transcript and /workers details, leaving
// the footer for bounded counts. Live runtime snapshots include managed work.
func (m *conversationModel) refreshJobChip() {
	running, queued := 0, 0
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
	m.jobStatus = ""
	if running+queued > 0 {
		m.jobStatus = workersCountLabel(running, queued, m.cfg == nil || !m.cfg.VoiceWorkersDisabled)
	}
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
		return truncateJobLine(v.task)
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
