package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

const defaultSpawnAck = "Working on it."

// workNarrationMilestones is the voice_work_narration setting that speaks one
// line per closed fold row. The default, status_only, speaks nothing until the
// user asks what is happening.
const workNarrationMilestones = "milestones"

// spawnClarifier is what the assistant says instead of spawning on an assent
// that has no conversation behind it.
const spawnClarifier = "I did not catch what you would like me to work on. What should I do?"

// Agency wires spawn/abort/status routes to the async job table and worker brain.
type Agency struct {
	pipeline *Pipeline
	jobs     *job.Table
	worker   brain.Provider
	cfg      *config.Config
	ack      string
	narrator *Narrator
	// work folds a job's tool calls into rows: what "what's happening" answers
	// with, and what milestone narration speaks one line of.
	work *workfold.Tracker

	mu            sync.Mutex
	pending       []pendingDelivery
	workerInitErr error
}

// pendingDelivery is one completed job waiting to be spoken when the pipeline
// is idle. A slice, not a single string: queued jobs finish sequentially, but
// playback or a foreground turn can delay delivery, and a later completion
// must not overwrite an earlier one.
type pendingDelivery struct {
	JobID string
	Text  string
}

// NewAgency constructs the spawn coordinator. worker may be nil when construction
// failed; spawn will surface the error at job start.
func NewAgency(p *Pipeline, jobs *job.Table, worker brain.Provider, cfg *config.Config, workerErr error) *Agency {
	ack := defaultSpawnAck
	if cfg != nil && strings.TrimSpace(cfg.ActivePersona) == "jarvis" {
		ack = "Working on it, sir."
	}
	a := &Agency{
		pipeline:      p,
		jobs:          jobs,
		worker:        worker,
		cfg:           cfg,
		ack:           ack,
		work:          workfold.New(),
		workerInitErr: workerErr,
	}
	a.narrator = NewNarrator(a)
	return a
}

// Wire attaches route handlers and job callbacks to the pipeline.
func (a *Agency) Wire() {
	if a == nil || a.pipeline == nil || a.jobs == nil {
		return
	}
	a.pipeline.Agency = a
	a.pipeline.JobActive = a.jobs.Active
	a.jobs.OnDone(a.onJobDone)
	a.jobs.OnQueued(a.onJobQueued)
	a.jobs.OnStart(a.onJobStarted)
	if a.narrator != nil {
		a.narrator.Wire()
	}
}

// HandleRoute executes spawn, job.abort, and job.status routes. OnRoute fires
// exactly once per call, after the handler, for every kind — including
// job.status and a spawn whose job failed to start — so an observer sees one
// decision per routed turn.
func (a *Agency) HandleRoute(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if a == nil {
		return
	}
	switch d.Kind {
	case RouteSpawn:
		a.handleSpawn(ctx, turn, metrics, d)
	case RouteNarrate, RouteNarratePause, RouteNarrateResume, RouteNarrateSkip, RouteNarrateStop:
		if a.narrator != nil {
			a.narrator.HandleRoute(ctx, turn, metrics, d)
		}
	case RouteJobAbort:
		a.handleAbort()
	case RouteJobStatus:
		a.speakJobStatus(ctx, turn, metrics)
	}
	if a.pipeline.OnRoute != nil {
		a.pipeline.OnRoute(d)
	}
}

func (a *Agency) handleSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	task := strings.TrimSpace(d.Transcript)
	history := a.liveHistory()

	// An assent with nothing behind it is not a task. Spawning on it hands a
	// worker with shell tools a prompt like "Yes." and lets it decide what
	// that meant — which is exactly how one went looking through the CLI's
	// session store and acted on somebody else's thread.
	if spawnLacksTask(history, task) {
		a.speakSpawnClarifier(ctx, turn, metrics)
		a.pipeline.emit(events.Info{Message: "spawn declined: no task in the utterance and no conversation behind it"})
		return
	}

	if task == "" {
		task = "background task"
	}
	a.speakAck(ctx, turn, metrics)
	_, err := a.jobs.Start(ctx, job.Spec{
		// Task is the label the user hears and sees; the brief is what the
		// worker reads. They were the same string, which is why the worker
		// only ever received one utterance.
		Task: task,
		Run:  a.workerRun(spawnBrief(history, d.Transcript)),
	})
	if err != nil {
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
	}
}

// onJobQueued runs inside Table.Start, so JobQueued precedes everything the
// job publishes. onJobStarted runs on the job's goroutine right before its run
// function, so JobStarted follows the previous job's terminal event and
// precedes this job's tool and terminal events. A spawn behind a running job
// is therefore queued, not started, until that job is out of the way.
func (a *Agency) onJobQueued(id, task string) {
	a.pipeline.emit(events.JobQueued{ID: id, Task: task})
}

func (a *Agency) onJobStarted(id, task string) {
	a.pipeline.emit(events.JobStarted{ID: id, Task: task})
}

// liveHistory is the talking brain's conversation, or nil when there is no
// brain to ask. A worker that gets nothing is handled by spawnLacksTask.
func (a *Agency) liveHistory() []brain.Turn {
	if a.pipeline == nil || a.pipeline.Brain == nil {
		return nil
	}
	return a.pipeline.Brain.History()
}

// speakSpawnClarifier asks rather than guesses.
func (a *Agency) speakSpawnClarifier(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, spawnClarifier); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("clarifier speech failed: %v", err)})
	}
}

func (a *Agency) workerRun(brief string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		worker, err := a.workerProvider()
		if err != nil {
			return "", err
		}
		worker = a.freshWorker(worker)
		tools := true
		if a.cfg != nil {
			tools = a.cfg.VoiceToolsEnabled
		}
		opts := brain.WorkerStreamOptions(tools)
		opts.OnToolStart, opts.OnToolEnd = a.foldingToolHooks(job.IDFromContext(ctx))
		return worker.ThinkFull(ctx, brief, opts)
	}
}

// freshWorker gives a job a conversation of its own: no history and no CLI
// session from the job before it, and nothing shared with a job running
// alongside. A provider that cannot fork is cleared instead, and only while
// it is the one job running, so a neighbour's turn is not wiped from under it.
func (a *Agency) freshWorker(w brain.Provider) brain.Provider {
	if f, ok := w.(brain.FreshProvider); ok {
		return f.Fresh()
	}
	running := 0
	for _, info := range a.jobs.Status() {
		if info.State == job.StateRunning {
			running++
		}
	}
	if running <= 1 {
		w.ClearHistory()
	}
	return w
}

// foldingToolHooks emits the job's tool events and folds them on the way past.
// Feeding the tracker here rather than from a bus subscription keeps the fold
// in step with the emit — the closed row milestone narration speaks is the
// return of the same Add — and needs no bus to exist.
func (a *Agency) foldingToolHooks(jobID string) (func(brain.ToolCall), func(brain.ToolResult)) {
	emitStart, emitEnd := a.pipeline.jobToolHooks(jobID)
	start := func(call brain.ToolCall) {
		emitStart(call)
		if closed := a.work.Add(toolStartedEvent(jobID, call)); closed != nil {
			a.narrateRow(jobID, *closed)
		}
	}
	end := func(res brain.ToolResult) {
		emitEnd(res)
		a.work.Finish(toolFinishedEvent(jobID, res))
	}
	return start, end
}

// narrateRow speaks one short line for a row that just closed, under the same
// idle gate as a job summary. Silent unless voice_work_narration is milestones.
func (a *Agency) narrateRow(jobID string, row workfold.Row) {
	if a.cfg == nil || a.cfg.VoiceWorkNarration != workNarrationMilestones {
		return
	}
	a.enqueueSummary(jobID, capitalize(row.String())+".")
	a.tryFlushPending()
}

// closeFold ends the job's fold, speaking its last row under milestones.
// narrate is false for an abort: cancelling stays silent (D5), and what an
// abort says is slice 3's to decide.
func (a *Agency) closeFold(jobID string, narrate bool) {
	rows := a.work.Close(jobID)
	if !narrate || len(rows) == 0 {
		return
	}
	a.narrateRow(jobID, rows[len(rows)-1])
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func (a *Agency) workerProvider() (brain.Provider, error) {
	if a.worker != nil {
		return a.worker, nil
	}
	if a.workerInitErr != nil {
		return nil, a.workerInitErr
	}
	return nil, fmt.Errorf("worker provider is not configured")
}

func (a *Agency) handleAbort() {
	st := a.jobs.Status()
	for _, info := range st {
		if info.State == job.StateRunning || info.State == job.StateQueued {
			a.jobs.Abort(info.ID)
		}
	}
}

func (a *Agency) speakJobStatus(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	var running, queued []job.Info
	for _, info := range a.jobs.Status() {
		switch info.State {
		case job.StateRunning:
			running = append(running, info)
		case job.StateQueued:
			queued = append(queued, info)
		}
	}
	line := "No job is running."
	switch {
	case len(running) > 0:
		first := running[0]
		line = fmt.Sprintf("Still working on %s.", truncateJobTask(first.Task))
		if done := a.work.Summary(first.ID); done != "" {
			// The middle dot is a screen separator; spoken, it is a comma.
			line += fmt.Sprintf(" So far: %s.", strings.ReplaceAll(done, " · ", ", "))
		}
	case len(queued) > 0:
		line = fmt.Sprintf("Queued: %s.", truncateJobTask(queued[0].Task))
		queued = queued[1:]
	}
	if more := jobCounts(len(running)-1, len(queued)); more != "" {
		line += " " + more
	}
	a.pipeline.speakStandalone(ctx, turn, metrics, line)
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

func (a *Agency) speakAck(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, a.ack); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("ack speech failed: %v", err)})
	}
}

func (a *Agency) onJobDone(id string, state job.State, result string, err error) {
	if a.narrator != nil && a.narrator.handleJobDone(id, state, result, err) {
		return
	}
	task, elapsed := a.jobInfo(id)
	// The fold closes and everything spoken is queued before the terminal
	// event goes out: the queue is drained at the end of this function, and an
	// observer that reacts to the event should see the delivery it announces.
	// Milestones lead the result — what the worker was doing when it stopped
	// is not an afterthought to its answer.
	a.closeFold(id, state != job.StateAborted)
	switch state {
	case job.StateDone:
		a.enqueueSummary(id, a.cleanSummary(result))
		a.pipeline.emit(events.JobFinished{ID: id, Task: task, Summary: result, Elapsed: elapsed})
	case job.StateFailed:
		msg := "That did not work."
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			msg = truncateJobTask(err.Error())
		}
		a.enqueueSummary(id, msg)
		a.pipeline.emit(events.JobFailed{ID: id, Task: task, Err: msg, Elapsed: elapsed})
	case job.StateAborted:
		// A cancel is visible on the bus and silent in speech: the user did
		// it. A budget stop is the one abort nobody asked for, so it speaks.
		reason := events.AbortCancelled
		if errors.Is(err, job.ErrBudget) {
			reason = events.AbortBudget
			a.enqueueSummary(id, fmt.Sprintf("I stopped %s after %s. It ran past its budget.",
				truncateJobTask(task), spokenDuration(elapsed)))
		}
		a.pipeline.emit(events.JobAborted{ID: id, Task: task, Elapsed: elapsed, Reason: reason})
	}
	a.tryFlushPending()
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

func (a *Agency) enqueueSummary(id, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "That finished with nothing to report."
	}
	a.mu.Lock()
	a.pending = append(a.pending, pendingDelivery{JobID: id, Text: text})
	a.mu.Unlock()
}

func (a *Agency) cleanSummary(raw string) string {
	cleaned := strings.TrimSpace(brain.CleanForVoice(raw))
	if speakableSegment(cleaned) {
		return cleaned
	}
	if strings.TrimSpace(raw) == "" {
		return "That finished with nothing to report."
	}
	return cleaned
}

func (a *Agency) tryFlushPending() {
	for {
		a.mu.Lock()
		if len(a.pending) == 0 {
			a.mu.Unlock()
			return
		}
		if !a.canSpeakIdleSummary() {
			a.mu.Unlock()
			return
		}
		next := a.pending[0]
		a.pending = a.pending[1:]
		a.mu.Unlock()
		if err := a.pipeline.speakStandalone(context.Background(), nil, nil, next.Text); err != nil {
			a.pipeline.emit(events.Info{Message: fmt.Sprintf("job %s summary speech failed: %v", next.JobID, err)})
		}
	}
}

func (a *Agency) canSpeakIdleSummary() bool {
	if a.pipeline.NarrationActive != nil && a.pipeline.NarrationActive() {
		return false
	}
	if a.pipeline.Player != nil && a.pipeline.Player.IsPlaying() {
		return false
	}
	if a.pipeline.activeTurns.Load() > 0 {
		return false
	}
	return true
}

func (a *Agency) AfterTurn() {
	if a == nil {
		return
	}
	if a.narrator != nil {
		a.narrator.tryFlushPending()
	}
	a.tryFlushPending()
}

func truncateJobTask(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 120 {
		return s
	}
	return s[:117] + "..."
}

// activeTurns counts in-flight RunTurn / RunTurnTextMode calls for idle summary gating.
func (p *Pipeline) incActiveTurn() {
	if p == nil {
		return
	}
	p.activeTurns.Add(1)
}

func (p *Pipeline) decActiveTurn() {
	if p == nil {
		return
	}
	p.activeTurns.Add(-1)
	if p.Agency != nil {
		p.Agency.AfterTurn()
	}
}
