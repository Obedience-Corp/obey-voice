package tui

import (
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Background work rendering. A worker's tool calls belong to the job, not to
// the conversation: they go to the activity feed and the footer chip, and never
// touch the status line, the orb, or the transcript — otherwise a job running
// behind a live turn hijacks both.

func (m *conversationModel) jobStarted(e events.JobStarted) {
	m.jobTask = e.Task
	m.setJobChip("")
	m.appendActivity("job", "started "+e.Task, 0)
}

func (m *conversationModel) jobEnded(detail string, elapsed time.Duration) {
	m.jobTask = ""
	m.jobStatus = ""
	m.appendActivity("job", detail, elapsed)
}

func (m *conversationModel) jobToolStarted(e events.ToolCallStarted) {
	m.setJobChip(e.Name)
	m.appendActivity("job", jobToolDetail(e.Name, e.Summary), 0)
}

func (m *conversationModel) jobToolFinished(e events.ToolCallFinished) {
	m.setJobChip(e.Name)
	detail := e.Name + " done"
	if e.Err != "" {
		detail = e.Name + " failed: " + e.Err
	} else if e.Preview != "" {
		detail += " → " + e.Preview
	}
	m.appendActivity("job", detail, e.Elapsed)
}

// setJobChip rewrites the footer chip, keeping the task text from JobStarted
// and appending the tool the worker is on.
func (m *conversationModel) setJobChip(name string) {
	chip := "Working: " + truncateJobLine(m.jobTask)
	if name != "" {
		chip += " · " + name
	}
	m.jobStatus = chip
}

func jobToolDetail(name, summary string) string {
	if summary == "" {
		return name
	}
	return name + " (" + summary + ")"
}
