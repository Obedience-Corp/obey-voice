package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// handleAbort is the exact-phrase cancel: every job in the conversation
// ends as one act, and the host says so once, here in the turn that asked.
// The done callback stays silent for a cancel: three jobs cancelled together
// are one act, and that callback also carries the budget stop, which has its
// own line. The line waits briefly for the cancelled workers to return so it
// can say whether they did.
func (a *Agency) handleAbort(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	receipt := a.ControlWork(WorkActor{Kind: WorkActorPhrase}, WorkRequest{Action: WorkCancel, All: true})
	receipt = a.awaitExits(ctx, receipt)
	text := phraseCancelLine(receipt)
	if a.workEnabled() {
		text = inboxCancelLine(receipt)
	}
	if metrics != nil {
		metrics.routedNote = a.cancelCheckpoint(receipt)
	}
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, text); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("cancel speech failed: %v", err)})
	}
}

// phraseCancelLine is what a spoken cancel says outside the work inbox.
func phraseCancelLine(r WorkReceipt) string {
	var ran []time.Duration
	stopping, queued, asked := 0, 0, 0
	var refused []string
	for _, item := range r.Jobs {
		switch {
		case item.Managed && item.Outcome == ControlRequested:
			asked++
		case item.Managed && item.Outcome == ControlRejected:
			refused = append(refused, item.Reason)
		case item.Queued:
			queued++
		case item.Outcome == ControlStopped || item.Outcome == ControlStopping:
			ran = append(ran, item.Ran)
			if item.Outcome == ControlStopping {
				stopping++
			}
		}
	}
	var parts []string
	if len(ran) > 0 || queued > 0 || asked+len(refused) == 0 {
		parts = append(parts, cancelLine(ran, stopping, queued))
	}
	switch {
	case asked == 1:
		parts = append(parts, "Asked Obey to cancel the delegated task.")
	case asked > 1:
		parts = append(parts, fmt.Sprintf("Asked Obey to cancel %d delegated tasks.", asked))
	}
	if len(refused) > 0 {
		parts = append(parts, "Obey could not cancel the delegated task: "+strings.Join(refused, "; ")+".")
	}
	return strings.Join(parts, " ")
}

// inboxCancelLine is what a spoken cancel says with the work inbox on.
func inboxCancelLine(r WorkReceipt) string {
	if err := r.Err(); err != nil {
		return "Some work could not be cancelled: " + err.Error()
	}
	text := "Cancellation requested."
	switch n := len(r.Stopping()); {
	case n == 1:
		text = "Cancellation requested; the worker is still stopping."
	case n > 1:
		text = fmt.Sprintf("Cancellation requested; %d workers are still stopping.", n)
	}
	return text + " Completed results remain in the work inbox."
}

// cancelLine is abortLine when every cancelled worker has returned. When
// some have not, it says the work was cancelled rather than stopped, and
// how many workers are still stopping.
func cancelLine(ran []time.Duration, stopping, queued int) string {
	if stopping == 0 || len(ran) == 0 {
		return abortLine(ran, queued)
	}
	longest := slices.Max(ran)
	var lead, tail string
	switch n := len(ran); {
	case n == 1 && longest < time.Second:
		lead = "Cancelled"
	case n == 1:
		lead = "Cancelled after " + spokenDuration(longest)
	case longest < time.Second:
		lead = fmt.Sprintf("Cancelled %d jobs", n)
	default:
		lead = fmt.Sprintf("Cancelled %d jobs. The longest ran %s", n, spokenDuration(longest))
	}
	switch {
	case len(ran) == 1:
		tail = "the worker is still stopping."
	case stopping == len(ran):
		tail = "the workers are still stopping."
	case stopping == 1:
		tail = "one worker is still stopping."
	default:
		tail = fmt.Sprintf("%d workers are still stopping.", stopping)
	}
	line := lead + "; " + tail
	if queued > 0 {
		line += fmt.Sprintf(" Dropped %s.", plural(queued, "queued job"))
	}
	return line
}

// cancelCheckpoint is what each cancelled running job had done, recorded
// with the host's cancel line in the conversation history but never spoken,
// so the persona can later say what the job got through. It says the job
// did not finish, so it is never taken for the job's answer.
func (a *Agency) cancelCheckpoint(r WorkReceipt) string {
	var notes []string
	for _, item := range r.Jobs {
		if item.Managed || item.Queued || (item.Outcome != ControlStopped && item.Outcome != ControlStopping) {
			continue
		}
		done := a.work.Summary(item.ID)
		if done == "" {
			continue
		}
		task, _ := a.jobInfo(item.ID)
		notes = append(notes, fmt.Sprintf("[%s (%s) was cancelled before it finished. Done so far: %s.]", item.ID, spokenTaskLabel(task), recentProgress(done)))
	}
	return strings.Join(notes, " ")
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
		if info.State == job.StateRunning {
			fact.SameOutput = origin.sameOutput
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
