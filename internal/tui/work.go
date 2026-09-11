package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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
	task    string
	running bool
	tool    string // the tool the worker is on, until the fold has a row
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
	m.renderJobItemLabel(id, label, suffix)
	delete(m.jobItems, id)
	m.forgetJob(id)
	m.refreshJobChip()
}

func (m *conversationModel) jobToolStarted(e events.ToolCallStarted) {
	m.workTracker().Add(e)
	m.noteJobTool(e.JobID, e.Name)
	m.appendActivity("job", jobToolDetail(e.Name, e.Summary), 0)
	m.renderJobItem(e.JobID, "")
}

func (m *conversationModel) jobToolFinished(e events.ToolCallFinished) {
	if _, ok := m.jobs[e.JobID]; !ok {
		return
	}
	m.workTracker().Finish(e)
	m.noteJobTool(e.JobID, e.Name)
	m.appendActivity("job", toolOutcomeDetail(e), e.ResultElapsed())
	m.renderJobItem(e.JobID, "")
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

// runningJob is the job the chip names: the earliest one that has started.
func (m *conversationModel) runningJob() (string, *jobView) {
	for _, id := range m.jobOrder {
		if v := m.jobs[id]; v != nil && v.running {
			return id, v
		}
	}
	return "", nil
}

// refreshJobChip rewrites the footer chip from job state. The first running
// job names the chip, with its folded summary (or the tool it is on until the
// fold has a row); jobs running alongside it and jobs waiting are counted.
func (m *conversationModel) refreshJobChip() {
	runningID, running := m.runningJob()
	moreRunning, queued := 0, 0
	firstQueued := ""
	for _, id := range m.jobOrder {
		v := m.jobs[id]
		if v == nil {
			continue
		}
		if v.running {
			if id != runningID {
				moreRunning++
			}
			continue
		}
		if firstQueued == "" {
			firstQueued = v.task
		}
		queued++
	}
	chip := ""
	switch {
	case running != nil:
		chip = "Working: " + truncateJobLine(running.task)
		detail := m.workTracker().Summary(runningID)
		if detail == "" {
			detail = running.tool
		}
		if detail != "" {
			chip += " · " + truncateJobLine(detail)
		}
	case queued > 0:
		chip = "Queued: " + truncateJobLine(firstQueued)
		queued--
	}
	var extras []string
	if moreRunning > 0 {
		extras = append(extras, fmt.Sprintf("+%d running", moreRunning))
	}
	if queued > 0 {
		extras = append(extras, fmt.Sprintf("+%d queued", queued))
	}
	if len(extras) > 0 {
		chip += " (" + strings.Join(extras, ", ") + ")"
	}
	m.jobStatus = chip
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
	line := dimStyle.Render("  ⚙ " + id + " · " + label + suffix)
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
