package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// controlTurn is what a live turn froze when it began, for a job_control call
// its model may make: the turn's identity, the acting persona, the
// conversation, the jobs its prompt named, and what the user said.
type controlTurn struct {
	// token is the turn's grant, zero when it has none; foreground is the
	// number the turn was entered with.
	token      uint64
	foreground uint64
	persona    string
	scope      work.WorkScope
	listed     []string
	bareStop   bool
}

// freezeControlTurn records a live turn's authority before its model request.
// A turn is granted control only when the provider answering it has job
// control on, the same setting that offered the tool, and it answers a person
// on the direct router with tools. listed is computed from the same facts and by the same
// pass that render them into the prompt.
// command is the user's request with a recognized address taken off.
func (p *Pipeline) freezeControlTurn(ctx context.Context, metrics *turnMetrics, opts brain.StreamOptions, command, original string) {
	if metrics == nil || p.Agency == nil {
		return
	}
	cfg := p.Agency.currentConfig()
	persona, ok := p.livePersona()
	names := []string{persona, metrics.response.DisplayName}
	if cfg != nil {
		names = append(names, cfg.AgentName)
	}
	frozen := &controlTurn{scope: p.ConversationScope(),
		bareStop: bareStop(command, names) || bareStop(original, names) || bareStop(metrics.recognized, names)}
	frozen.foreground, _ = ctx.Value(foregroundKey{}).(uint64)
	if frozen.foreground == 0 || frozen.foreground != p.foreground.Load() {
		ok = false
	}
	if id := metrics.response.PersonaID; ok && id != "" && personaKey(id) != persona {
		ok = false
	}
	if ok && brain.DecodesJobControl(p.Brain) && opts.JobControl && opts.ToolsEnabled && opts.RouterToolsPolicy == brain.RouterToolsDirect {
		frozen.token = p.controlTurns.Add(1)
		frozen.persona = persona
		frozen.listed = brain.ListedWorkerFactIDs(opts.WorkerFacts)
		p.liveControl.Store(frozen.token)
	}
	metrics.controlTurn = frozen
}

// revokeJobControl ends any turn's grant: the live persona binding changed.
func (p *Pipeline) revokeJobControl() { p.liveControl.Store(0) }

type foregroundKey struct{}

// enterForeground numbers a foreground turn as it is entered, before anything
// else it does, which ends the grant of every turn entered before it.
func (p *Pipeline) enterForeground(ctx context.Context) context.Context {
	return context.WithValue(ctx, foregroundKey{}, p.foreground.Add(1))
}

// Lines the host says, or shows, for a persona's job control.
const (
	bareStopNotice     = `A bare "stop" only silences speech; say "cancel" to end a job.`
	notLiveNotice      = "A job control arrived after its turn stopped, so no job was changed."
	controlFailedLine  = "I couldn't do that."
	notOwnLine         = "I can only do that for my own jobs."
	nothingRunningLine = "I have no job running."
	cannotTellLine     = "I can't tell which job you mean from my list."
	alreadyDoneLine    = "That job had already finished."
	steerSentLine      = "Sent; it reads it at its next step."
	steerBriefedLine   = "Added to its brief; it reads it when it starts."
	reportStartingLine = "It's just starting, so there's nothing to report yet."
	reportQueuedLine   = "That job hasn't started yet, so there's nothing to report."
	reportFinishLine   = "It's already finishing its answer."
)

// controlOutcome is what the host decided about one job control, for the
// turn's line and for observers.
type controlOutcome struct {
	Control *brain.JobControl
	// Refusal is why nothing was asked of a job; empty when ControlWork ran.
	Refusal string
	Receipt *WorkReceipt
	Line    string
	Notice  string
}

// Host refusals, after the brain's own.
const (
	refusedBareStop = "bare_stop"
	refusedNotLive  = "not_live"
	refusedNoJobs   = "no_jobs"
	refusedUnlisted = "unlisted"
	refusedNotOwn   = "not_own"
)

// handleJobControl settles the job_control call a live turn ended at: it
// checks the call against what the turn froze, asks ControlWork at most once
// with the persona as actor, and speaks one line of its own in the persona's
// voice after whatever the persona had already said.
func (a *Agency) handleJobControl(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	p := a.pipeline
	control := metrics.jobControl
	out := a.decideJobControl(ctx, metrics, control)
	if spoken := strings.TrimSpace(brain.CleanForVoice(controlSpoken(control))); spoken != "" {
		if !metrics.controlSpoken && out.Refusal != refusedBareStop && out.Refusal != refusedNotLive {
			p.speakUnrecorded(ctx, turn, metrics, speakableFromResponse(p.gateForSpeech(spoken, metrics), responseMentionName(ctx)))
		}
		if metrics.routedSpeech != nil {
			*metrics.routedSpeech = append(*metrics.routedSpeech, spoken)
		}
	}
	if out.Notice != "" {
		p.emit(events.Info{Message: out.Notice})
	}
	if out.Line != "" {
		if err := p.speakStandalone(ctx, turn, metrics, out.Line); err != nil {
			p.emit(events.Info{Message: fmt.Sprintf("job control speech failed: %v", err)})
		}
	}
	if a.onJobControl != nil {
		a.onJobControl(out)
	}
}

func controlSpoken(c *brain.JobControl) string {
	if c == nil {
		return ""
	}
	return c.Spoken
}

func (a *Agency) decideJobControl(ctx context.Context, metrics *turnMetrics, control *brain.JobControl) controlOutcome {
	frozen := metrics.controlTurn
	out := controlOutcome{Control: control}
	refuse := func(reason, line, notice string) controlOutcome {
		out.Refusal, out.Line, out.Notice = reason, line, notice
		return out
	}
	if frozen != nil && frozen.bareStop {
		return refuse(refusedBareStop, "", bareStopNotice)
	}
	if ctx.Err() != nil || metrics.interrupted || metrics.speechStopped() {
		return refuse(refusedNotLive, "", notLiveNotice)
	}
	// A call the request did not offer, as on a turn with job control off,
	// runs nothing whatever the turn's grant.
	if control == nil || control.Refusal == brain.JobControlNotOffered {
		return refuse(string(brain.JobControlNotOffered), controlFailedLine, "")
	}
	if !a.controlTurnLive(metrics, frozen) {
		return refuse(refusedNotLive, "", notLiveNotice)
	}
	if control.Request == nil {
		reason := control.Refusal
		switch reason {
		case brain.JobControlNoJob, brain.JobControlAmbiguous:
			return refuse(string(reason), a.whichJobLine(frozen), "")
		}
		return refuse(string(reason), controlFailedLine, "")
	}
	req := control.Request
	if !slices.Contains(frozen.listed, req.JobID) {
		reason := refusedUnlisted
		if len(a.controllableJobs(frozen)) == 0 {
			reason = refusedNoJobs
		}
		return refuse(reason, a.whichJobLine(frozen), "")
	}
	if !a.personaOwns(frozen.persona, req.JobID) {
		return refuse(refusedNotOwn, notOwnLine, "")
	}
	named := a.namedJob(frozen, req.JobID)
	receipt := a.ControlWork(WorkActor{Kind: WorkActorPersona, Persona: frozen.persona}, WorkRequest{Action: WorkAction(req.Action), JobID: req.JobID, Instruction: req.Instruction})
	if req.Action == brain.JobControlCancel {
		receipt = a.awaitExits(ctx, receipt)
		metrics.routedNote = a.cancelCheckpoint(receipt)
	}
	out.Receipt = &receipt
	out.Line = a.controlLine(frozen, receipt, named)
	return out
}

// namedJob is how a line names job id when the turn's list held more than one
// of the persona's own jobs, so the user hears which one was acted on; empty
// otherwise.
func (a *Agency) namedJob(frozen *controlTurn, id string) string {
	own := 0
	for _, listed := range frozen.listed {
		if a.personaOwns(frozen.persona, listed) {
			own++
		}
	}
	if own < 2 {
		return ""
	}
	task, _ := a.jobInfo(id)
	return "the job for " + spokenTaskLabel(task)
}

// controlTurnLive spends the turn's grant: it holds only while the turn that
// froze it is the live one, was not stopped, and its conversation and
// persona are still the ones it froze.
func (a *Agency) controlTurnLive(metrics *turnMetrics, frozen *controlTurn) bool {
	p := a.pipeline
	if frozen == nil || frozen.token == 0 || !p.liveControl.CompareAndSwap(frozen.token, 0) || frozen.foreground != p.foreground.Load() {
		return false
	}
	if metrics.interrupted || metrics.speechStopped() {
		return false
	}
	if !frozen.scope.Valid() || frozen.scope != p.ConversationScope() || p.ConversationRetired() {
		return false
	}
	persona, ok := p.livePersona()
	return ok && persona == frozen.persona
}

// bareStop reports an utterance that is only "stop", case, punctuation and
// spacing aside, or "stop" followed by one of the persona's own names.
func bareStop(utterance string, names []string) bool {
	words := spokenWords(utterance)
	if len(words) > 1 {
		for _, name := range names {
			if name := spokenWords(name); len(name) > 0 && len(words) == len(name)+1 && slices.Equal(words[1:], name) {
				words = words[:1]
				break
			}
		}
	}
	return slices.Equal(words, []string{"stop"})
}

// spokenWords are the words of s in lower case, without punctuation.
func spokenWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// personaOwns reports whether persona started job id in the current
// conversation, by the job's frozen origin. A job whose origin or starter is
// not on record is no persona's.
func (a *Agency) personaOwns(persona, id string) bool {
	a.mu.Lock()
	origin, ok := a.workerOrigins[id]
	a.mu.Unlock()
	if !ok || persona == "" {
		return false
	}
	starter, tagged := origin.admission["persona"]
	if !tagged || personaKey(starter) != persona {
		return false
	}
	p := a.pipeline
	return origin.Scope.Valid() && origin.Scope == p.ConversationScope() && !p.ConversationRetired()
}

// controllableJobs are the listed jobs the acting persona may control that
// are still running or queued, as the list named them.
func (a *Agency) controllableJobs(frozen *controlTurn) []job.Info {
	if frozen == nil {
		return nil
	}
	status := a.jobs.Status()
	var out []job.Info
	for _, id := range frozen.listed {
		if !a.personaOwns(frozen.persona, id) {
			continue
		}
		for _, info := range status {
			if info.ID == id && (info.State == job.StateRunning || info.State == job.StateQueued) {
				out = append(out, info)
			}
		}
	}
	return out
}

// personaHasJobs reports any running or queued job the persona started in
// this conversation.
func (a *Agency) personaHasJobs(persona string) bool {
	for _, info := range a.jobs.Status() {
		if (info.State == job.StateRunning || info.State == job.StateQueued) && a.personaOwns(persona, info.ID) {
			return true
		}
	}
	return false
}

// whichJobLine asks which job was meant, naming only the jobs the persona
// may control. With none of them still running it says it has no job only
// when no job of its own is running at all, listed or not.
func (a *Agency) whichJobLine(frozen *controlTurn) string {
	jobs := a.controllableJobs(frozen)
	if len(jobs) == 0 {
		if frozen != nil && a.personaHasJobs(frozen.persona) {
			return cannotTellLine
		}
		return nothingRunningLine
	}
	labels := make([]string, len(jobs))
	counts := map[string]int{}
	for i, info := range jobs {
		labels[i] = spokenTaskLabel(info.Task)
		counts[labels[i]]++
	}
	running := map[string]int{}
	for i, info := range jobs {
		if info.State == job.StateRunning {
			running[labels[i]]++
		}
	}
	for i, info := range jobs {
		switch {
		case counts[labels[i]] < 2:
		case info.State == job.StateQueued:
			labels[i] += ", the queued one"
		case running[labels[i]] < 2:
			labels[i] += ", the running one"
		default:
			labels[i] += ", the one running " + spokenDuration(info.Elapsed())
		}
	}
	if len(labels) == 1 {
		return "Which job do you mean? Mine is " + labels[0] + "."
	}
	return "Which job do you mean: " + strings.Join(labels[:len(labels)-1], "; ") + "; or " + labels[len(labels)-1] + "?"
}

// controlLine is what the host says of a persona's control, from its receipt.
// named, when set, is the job the line is about.
func (a *Agency) controlLine(frozen *controlTurn, r WorkReceipt, named string) string {
	if named != "" {
		return a.namedControlLine(frozen, r, named)
	}
	if r.Rejected != "" || len(r.Jobs) != 1 {
		return controlFailedLine
	}
	item := r.Jobs[0]
	switch item.Outcome {
	case ControlFinished:
		return alreadyDoneLine
	case ControlUnknown:
		return a.whichJobLine(frozen)
	}
	switch r.Request.Action {
	case WorkCancel:
		switch item.Outcome {
		case ControlStopped, ControlStopping:
			if item.Queued {
				return cancelLine(nil, 0, 1)
			}
			stopping := 0
			if item.Outcome == ControlStopping {
				stopping = 1
			}
			return cancelLine([]time.Duration{item.Ran}, stopping, 0)
		}
	case WorkReport:
		return reportLine(item)
	case WorkSteer:
		switch item.Outcome {
		case ControlAccepted:
			return steerSentLine
		case ControlBriefed:
			return steerBriefedLine
		case ControlRejected:
			return "I couldn't pass that on: " + item.Reason + "."
		}
	}
	if item.Outcome == ControlRejected && item.Reason != "" {
		return "I couldn't do that: " + item.Reason + "."
	}
	return controlFailedLine
}

// namedControlLine is controlLine naming job, "the job for <task>".
func (a *Agency) namedControlLine(frozen *controlTurn, r WorkReceipt, job string) string {
	subject := strings.ToUpper(job[:1]) + job[1:]
	if r.Rejected != "" || len(r.Jobs) != 1 {
		return "I couldn't do that to " + job + "."
	}
	item := r.Jobs[0]
	switch item.Outcome {
	case ControlFinished:
		return subject + " had already finished."
	case ControlUnknown:
		return a.whichJobLine(frozen)
	}
	switch r.Request.Action {
	case WorkCancel:
		switch {
		case item.Queued:
			return "Dropped the queued " + strings.TrimPrefix(job, "the ") + "."
		case item.Outcome == ControlStopping && item.Ran < time.Second:
			return "Cancelled " + job + "; the worker is still stopping."
		case item.Outcome == ControlStopping:
			return "Cancelled " + job + " after " + spokenDuration(item.Ran) + "; the worker is still stopping."
		case item.Outcome == ControlStopped && item.Ran < time.Second:
			return "Stopped " + job + "."
		case item.Outcome == ControlStopped:
			return "Stopped " + job + " after " + spokenDuration(item.Ran) + "."
		}
	case WorkReport:
		return namedReportLine(item, job, subject)
	case WorkSteer:
		switch item.Outcome {
		case ControlAccepted:
			return "Sent that to " + job + "; it reads it at its next step."
		case ControlBriefed:
			return "Added that to the brief of " + job + "; it reads it when it starts."
		case ControlRejected:
			return "I couldn't pass that on to " + job + ": " + item.Reason + "."
		}
	}
	if item.Outcome == ControlRejected && item.Reason != "" {
		return "I couldn't do that to " + job + ": " + item.Reason + "."
	}
	return "I couldn't do that to " + job + "."
}

// namedReportLine is reportLine naming job.
func namedReportLine(item JobControl, job, subject string) string {
	switch {
	case item.Outcome == ControlAccepted && item.Checkpoint == noActivity:
		return "I asked " + job + " to report, and it's writing up what it has; it has recorded no tool activity yet."
	case item.Outcome == ControlAccepted:
		return "I asked " + job + " to report, and it's writing up what it has; so far: " + spokenActivity(item.Checkpoint) + "."
	case item.Reason == reportNoInput && item.Checkpoint != "" && item.Checkpoint != noActivity:
		return subject + " so far: " + spokenActivity(item.Checkpoint) + "; it can't be asked to stop, so the work continues."
	case item.Reason == reportNoInput:
		return subject + " has recorded no tool activity yet, and it can't be asked to stop, so the work continues."
	case item.Reason == reportStarting:
		return subject + " is just starting, so there's nothing to report yet."
	case item.Reason == reportNotStarted:
		return subject + " hasn't started yet, so there's nothing to report."
	case item.Reason == reportFinishing:
		return subject + " is already finishing its answer."
	case item.Reason != "":
		return "I couldn't do that to " + job + ": " + item.Reason + "."
	}
	return "I couldn't do that to " + job + "."
}

// reportLine says what a report found. A report the persona asked for may not
// be one the user asked for, so the line says the persona asked.
func reportLine(item JobControl) string {
	switch {
	case item.Outcome == ControlAccepted && item.Checkpoint == noActivity:
		return "I asked it to report, and it's writing up what it has; it has recorded no tool activity yet."
	case item.Outcome == ControlAccepted:
		return "I asked it to report, and it's writing up what it has; so far: " + spokenActivity(item.Checkpoint) + "."
	case item.Reason == reportNoInput && item.Checkpoint != "" && item.Checkpoint != noActivity:
		return "So far: " + spokenActivity(item.Checkpoint) + "; it can't be asked to stop, so the work continues."
	case item.Reason == reportNoInput:
		return "It has recorded no tool activity yet, and it can't be asked to stop, so the work continues."
	case item.Reason == reportStarting:
		return reportStartingLine
	case item.Reason == reportNotStarted:
		return reportQueuedLine
	case item.Reason == reportFinishing:
		return reportFinishLine
	case item.Reason != "":
		return "I couldn't do that: " + item.Reason + "."
	}
	return controlFailedLine
}

var activityCounts = regexp.MustCompile(` \([^)]*\)`)

// spokenActivity is a job's folded activity as a status line says it, without
// the per-outcome counts the screen shows.
func spokenActivity(summary string) string {
	return strings.ReplaceAll(activityCounts.ReplaceAllString(summary, ""), " · ", ", ")
}

// speakUnrecorded speaks a line already recorded in the turn's reply.
func (p *Pipeline) speakUnrecorded(ctx context.Context, turn *turnConductor, metrics *turnMetrics, sentence string) {
	if strings.TrimSpace(sentence) == "" {
		return
	}
	parts := metrics.routedSpeech
	metrics.routedSpeech = nil
	defer func() { metrics.routedSpeech = parts }()
	_ = p.speakStandalone(ctx, turn, metrics, sentence)
}
