package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// handleAbort ends every running and queued job and says so once, here in
// the turn that asked. The done callback stays silent for a cancel: three
// jobs cancelled together are one act, and that callback also carries the
// budget stop, which has its own line.
func (a *Agency) handleAbort(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	if a.workEnabled() {
		var cancellationErrors []error
		for _, item := range a.WorkSnapshot().Jobs {
			if item.State != work.JobTerminal {
				cancellationErrors = append(cancellationErrors, a.CancelWork(item.Key.JobID))
			}
		}
		text := "Cancellation requested. Completed results remain in the work inbox."
		if err := errors.Join(cancellationErrors...); err != nil {
			text = "Some work could not be cancelled: " + err.Error()
		}
		_ = a.pipeline.speakStandalone(ctx, turn, metrics, text)
		return
	}
	var ran []time.Duration
	var queued, running []string
	for _, info := range a.jobs.Status() {
		switch info.State {
		case job.StateRunning:
			running = append(running, info.ID)
			ran = append(ran, info.Elapsed())
		case job.StateQueued:
			queued = append(queued, info.ID)
		}
	}
	// Queued first, so the slot a running job frees has nothing to pump.
	for _, id := range append(queued, running...) {
		a.jobs.Abort(id)
	}
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, abortLine(ran, len(queued))); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("cancel speech failed: %v", err)})
	}
}

// abortLine is what a cancel says: how long the work ran, and what never got
// to start.
func abortLine(ran []time.Duration, queued int) string {
	var parts []string
	var longest time.Duration
	for _, d := range ran {
		if d > longest {
			longest = d
		}
	}
	switch n := len(ran); {
	case n == 1 && longest < time.Second:
		parts = append(parts, "Stopped.")
	case n == 1:
		parts = append(parts, fmt.Sprintf("Stopped after %s.", spokenDuration(longest)))
	case n > 1 && longest < time.Second:
		parts = append(parts, fmt.Sprintf("Stopped %d jobs.", n))
	case n > 1:
		parts = append(parts, fmt.Sprintf("Stopped %d jobs. The longest ran %s.", n, spokenDuration(longest)))
	}
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("Dropped %s.", plural(queued, "queued job")))
	}
	if len(parts) == 0 {
		return "No job is running."
	}
	return strings.Join(parts, " ")
}

// speakJobStatus names the jobs the live persona may be told about and only
// counts the others, so a persona on a hosted backend never hears another
// persona's task or progress.
func (a *Agency) speakJobStatus(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	var running, queued []job.Info
	hiddenRunning, hiddenQueued := 0, 0
	for _, info := range a.jobs.Status() {
		if info.State != job.StateRunning && info.State != job.StateQueued {
			continue
		}
		a.mu.Lock()
		admission := a.workerOrigins[info.ID].admission
		a.mu.Unlock()
		shown := a.pipeline.receivesJob(admission)
		switch {
		case info.State == job.StateRunning && shown:
			running = append(running, info)
		case info.State == job.StateRunning:
			hiddenRunning++
		case shown:
			queued = append(queued, info)
		default:
			hiddenQueued++
		}
	}
	var parts []string
	switch {
	case len(running) > 0:
		first := running[0]
		parts = append(parts, fmt.Sprintf("Still working on %s.", spokenTaskLabel(first.Task)))
		if done := a.work.Summary(first.ID); done != "" {
			// The middle dot is a screen separator; spoken, it is a comma.
			parts = append(parts, fmt.Sprintf("So far: %s.", strings.ReplaceAll(done, " · ", ", ")))
		}
	case len(queued) > 0:
		parts = append(parts, fmt.Sprintf("Queued: %s.", spokenTaskLabel(queued[0].Task)))
		queued = queued[1:]
	}
	if more := jobCounts(len(running)-1, len(queued)); more != "" {
		parts = append(parts, more)
	}
	if others := otherJobs(hiddenRunning, hiddenQueued); others != "" {
		parts = append(parts, others)
	}
	line := "No job is running."
	if len(parts) > 0 {
		line = strings.Join(parts, " ")
	}
	// Standalone speech reports synthesis/playback errors on the event bus.
	_ = a.pipeline.speakStandalone(ctx, turn, metrics, line)
}

// activeJobFacts describes the running and queued jobs the live persona may
// be told about, its own first. The acknowledgement that starts a job is
// written by the host and leaves the conversation window long before a long
// job ends, so without these the persona answering has no record that the
// job exists.
func (a *Agency) activeJobFacts() []brain.WorkerFact {
	var facts []brain.WorkerFact
	for _, info := range a.jobs.Status() {
		if info.State != job.StateRunning && info.State != job.StateQueued {
			continue
		}
		a.mu.Lock()
		origin := a.workerOrigins[info.ID]
		a.mu.Unlock()
		if !a.pipeline.receivesJob(origin.admission) {
			continue
		}
		fact := brain.WorkerFact{
			ID:       info.ID,
			Tier:     factTier(origin.admission),
			State:    string(info.State),
			Task:     info.Task,
			Running:  info.Elapsed(),
			Progress: recentProgress(a.work.Summary(info.ID)),
			Own:      a.pipeline.startedByLivePersona(origin.admission),
		}
		if !fact.Own {
			fact.Starter = cmp.Or(strings.TrimSpace(origin.Envelope.DisplayName), strings.TrimSpace(origin.admission["persona"]))
		}
		facts = append(facts, fact)
	}
	slices.SortStableFunc(facts, func(x, y brain.WorkerFact) int {
		switch {
		case x.Own == y.Own:
			return 0
		case x.Own:
			return -1
		default:
			return 1
		}
	})
	return facts
}

func factTier(admission map[string]string) config.WorkerTierName {
	switch tier := config.WorkerTierName(admission["worker_tier"]); tier {
	case config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex:
		return tier
	default:
		return "legacy"
	}
}

// progressRows caps how much of a job's activity one request repeats.
const progressRows = 3

// recentProgress keeps the newest rows of a job's folded activity. A long job
// folds into many rows, and the newest say what it is doing now.
func recentProgress(summary string) string {
	rows := strings.Split(summary, " · ")
	if len(rows) <= progressRows {
		return strings.Join(rows, ", ")
	}
	return "earlier work, then " + strings.Join(rows[len(rows)-progressRows:], ", ")
}

// otherJobs counts the jobs a status line may not describe.
func otherJobs(running, queued int) string {
	state := "running"
	switch {
	case running == 0:
		state = "queued"
	case queued > 0:
		state = "running or queued"
	}
	switch n := running + queued; n {
	case 0:
		return ""
	case 1:
		return "One other job is " + state + "."
	default:
		return fmt.Sprintf("%d other jobs are %s.", n, state)
	}
}

// jobCounts phrases what is working or waiting besides the job just named.
func jobCounts(moreRunning, queued int) string {
	var parts []string
	if moreRunning > 0 {
		parts = append(parts, fmt.Sprintf("%d more running", moreRunning))
	}
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", queued))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Also " + strings.Join(parts, " and ") + "."
}

// spokenDuration phrases a duration for speech; "10m0s" is not a thing
// anyone says.
func spokenDuration(d time.Duration) string {
	d = d.Round(time.Second)
	secs := int(d.Seconds())
	if secs < 60 {
		return plural(secs, "second")
	}
	mins, rest := secs/60, secs%60
	if rest == 0 {
		return plural(mins, "minute")
	}
	return plural(mins, "minute") + " " + plural(rest, "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// spokenTaskWords caps how much of a task a status line repeats.
const spokenTaskWords = 10

// spokenTaskLabel is the task as speech names it: the first clause, at most
// spokenTaskWords words. The chip keeps the whole request; a status line
// that repeats twenty words verbatim takes longer to hear than the answer.
func spokenTaskLabel(task string) string {
	s := strings.TrimSpace(task)
	for _, sep := range []string{",", ";", " and then ", " then ", " after that "} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	words := strings.Fields(s)
	if len(words) > spokenTaskWords {
		return strings.Join(words[:spokenTaskWords], " ") + "…"
	}
	if label := strings.TrimRight(strings.Join(words, " "), ".!?:"); label != "" {
		return label
	}
	return "that"
}
