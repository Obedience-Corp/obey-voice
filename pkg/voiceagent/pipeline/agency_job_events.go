package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// onJobQueued runs inside Table.Start, so JobQueued precedes everything the
// job publishes. onJobStarted runs on the job's goroutine right before its run
// function, so JobStarted follows the previous job's terminal event and
// precedes this job's tool and terminal events. A spawn behind a running job
// is therefore queued, not started, until that job is out of the way.
func (a *Agency) onJobQueued(id, task string) {
	a.mu.Lock()
	origin := a.workerOrigins[id]
	origin.traceQueuedAt = time.Now()
	a.workerOrigins[id] = origin
	a.mu.Unlock()
	a.traceJobBoundary(id, "queued")
	if a.workEnabled() {
		// Register already published the canonical scoped admission.
		return
	}
	a.emitJobTrace(id, events.JobQueued{ID: id, Task: task})
}

func (a *Agency) onJobStarted(id, task string) {
	c, key, accepted := a.callbackKey(id)
	if !accepted {
		return
	}
	a.traceJobBoundary(id, "started")
	if c != nil {
		_ = c.Started(key)
		return // The canonical snapshot carries the admitted scope.
	}
	a.emitJobTrace(id, events.JobStarted{ID: id, Task: task})
}

func (a *Agency) onJobDone(id string, state job.State, result string, err error) {
	a.mu.Lock()
	releaseVoice := a.workerOrigins[id].releaseVoice
	a.mu.Unlock()
	if releaseVoice != nil {
		defer releaseVoice()
	}
	c, key, accepted := a.callbackKey(id)
	if !accepted {
		return
	}
	a.traceJobBoundary(id, string(state))
	if state == job.StateDone {
		result = cleanWorkerReply(result)
	}
	if errors.Is(err, brain.ErrWorkerBudgetExceeded) || errors.Is(err, job.ErrBudget) || errors.Is(err, context.DeadlineExceeded) {
		a.pipeline.emit(events.WorkerLimitReached{Scope: key.Scope, JobID: id, Message: errorText(err, "Worker limit reached"), Termination: "unknown"})
	}
	if !a.workEnabled() && a.narrator != nil && a.narrator.handleJobDone(id, state, result, err) {
		return
	}
	task, elapsed := a.jobInfo(id)
	refs := a.workerReplyMedia(id)
	if state != job.StateDone {
		refs = nil
	}
	// The fold closes and everything spoken is queued before the terminal
	// event goes out: the queue is drained at the end of this function, and an
	// observer that reacts to the event should see the delivery it announces.
	// Milestones lead the result — what the worker was doing when it stopped
	// is not an afterthought to its answer.
	a.closeFold(id, state == job.StateDone && !a.workEnabled())
	if c != nil {
		status := work.OutcomeSucceeded
		text := a.cleanSummary(result)
		termination := "known"
		startedAt, endedAt, terminalElapsed := a.jobTiming(id)
		switch state {
		case job.StateFailed:
			status, text = work.OutcomeFailed, errorText(err, result)
			if errors.Is(err, brain.ErrWorkerOutOfTime) {
				status, text = work.OutcomeTimedOut, outOfTimeText(terminalElapsed, err)
			}
		case job.StateAborted:
			status, text, termination = work.OutcomeCancelled, "Work was cancelled.", "unknown"
			if errors.Is(err, job.ErrBudget) {
				status, text = work.OutcomeTimedOut, outOfTimeText(terminalElapsed, err)
			}
		}
		if errors.Is(err, brain.ErrWorkerBudgetExceeded) || errors.Is(err, job.ErrBudget) || errors.Is(err, context.DeadlineExceeded) {
			termination = "unknown"
		}
		_, publishErr := c.Publish(key, work.Outcome{Scope: key.Scope, JobID: id, Status: status, Text: text, Attachments: refs, Termination: termination, StartedAt: startedAt, EndedAt: endedAt, Elapsed: terminalElapsed})
		if publishErr != nil {
			return
		}
		// Experimental work owns the inbox; it must never consume results by
		// speaking summaries from the legacy pending slice.
		if a.workEnabled() {
			a.workMu.Lock()
			delete(a.jobScopes, id)
			a.workMu.Unlock()
			return
		}
	}
	switch state {
	case job.StateDone:
		a.emitJobTrace(id, a.queueResult(id, task, result, refs, elapsed))
	case job.StateFailed:
		msg := "That did not work."
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			msg = err.Error()
		}
		a.emitJobTrace(id, events.JobFailed{ID: id, Task: task, Err: msg, Elapsed: elapsed})
	case job.StateAborted:
		// A cancel was spoken once by handleAbort, in the turn that asked;
		// saying it again here would be once per job. Budget failures are
		// also reported as text instead of unsolicited speech.
		reason := events.AbortCancelled
		if errors.Is(err, job.ErrBudget) {
			reason = events.AbortBudget
		}
		a.emitJobTrace(id, events.JobAborted{ID: id, Task: task, Elapsed: elapsed, Reason: reason})
	}
	a.tryFlushPending()
}

// outOfTimeText reports a job whose time ran out before the worker delivered
// its answer, with any part of the answer it had written.
func outOfTimeText(elapsed time.Duration, err error) string {
	text := fmt.Sprintf("Work ran out of time after %s, before it could write its answer.", spokenDuration(elapsed))
	var cut *brain.WorkerOutOfTimeError
	if errors.As(err, &cut) && cut.Partial != "" {
		text += " Partial answer: " + cut.Partial
	}
	return text
}

func errorText(err error, fallback string) string {
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		return err.Error()
	}
	return fallback
}

// jobInfo is the job's label and how long it ran.
func (a *Agency) jobInfo(id string) (string, time.Duration) {
	for _, info := range a.jobs.Status() {
		if info.ID == id {
			return info.Task, info.Elapsed()
		}
	}
	return "", 0
}

func (a *Agency) jobTiming(id string) (time.Time, time.Time, time.Duration) {
	for _, info := range a.jobs.Status() {
		if info.ID == id {
			return info.Started, info.Ended, info.TerminalElapsed
		}
	}
	return time.Time{}, time.Time{}, 0
}
