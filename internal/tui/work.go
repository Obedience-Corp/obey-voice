package tui

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Background work rendering. A worker's tool calls belong to the job, not to
// the conversation: they go to the activity feed and the footer chip, and never
// touch the status line, the orb, or the transcript — otherwise a job running
// behind a live turn hijacks both. State is kept per job: a spawn queued
// behind a running worker is counted on the chip but never relabels it.

// jobView is what the chip knows about one queued or running job.
type jobView struct {
	task    string
	running bool
	tool    string // the tool the worker is on, once it has called one
}

func (m *conversationModel) jobQueued(e events.JobQueued) {
	m.trackJob(e.ID, e.Task)
	m.appendActivity("job", "queued "+e.Task, 0)
	m.refreshJobChip()
}

func (m *conversationModel) jobStarted(e events.JobStarted) {
	m.trackJob(e.ID, e.Task).running = true
	m.appendActivity("job", "started "+e.Task, 0)
	m.refreshJobChip()
}

func (m *conversationModel) jobEnded(id, detail string, elapsed time.Duration) {
	m.forgetJob(id)
	m.appendActivity("job", detail, elapsed)
	m.refreshJobChip()
}

func (m *conversationModel) jobToolStarted(e events.ToolCallStarted) {
	m.noteJobTool(e.JobID, e.Name)
	m.appendActivity("job", jobToolDetail(e.Name, e.Summary), 0)
}

func (m *conversationModel) jobToolFinished(e events.ToolCallFinished) {
	m.noteJobTool(e.JobID, e.Name)
	detail := e.Name + " done"
	if e.Err != "" {
		detail = e.Name + " failed: " + e.Err
	} else if e.Preview != "" {
		detail += " → " + e.Preview
	}
	m.appendActivity("job", detail, e.Elapsed)
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
func (m *conversationModel) runningJob() *jobView {
	for _, id := range m.jobOrder {
		if v := m.jobs[id]; v != nil && v.running {
			return v
		}
	}
	return nil
}

// refreshJobChip rewrites the footer chip from job state. Only a running job
// names the chip; the ones waiting behind it are counted.
func (m *conversationModel) refreshJobChip() {
	running := m.runningJob()
	queued := 0
	firstQueued := ""
	for _, id := range m.jobOrder {
		if v := m.jobs[id]; v != nil && !v.running {
			if firstQueued == "" {
				firstQueued = v.task
			}
			queued++
		}
	}
	chip := ""
	switch {
	case running != nil:
		chip = "Working: " + truncateJobLine(running.task)
		if running.tool != "" {
			chip += " · " + running.tool
		}
	case queued > 0:
		chip = "Queued: " + truncateJobLine(firstQueued)
		queued--
	}
	if queued > 0 {
		chip += fmt.Sprintf(" (+%d queued)", queued)
	}
	m.jobStatus = chip
}

func jobToolDetail(name, summary string) string {
	if summary == "" {
		return name
	}
	return name + " (" + summary + ")"
}
