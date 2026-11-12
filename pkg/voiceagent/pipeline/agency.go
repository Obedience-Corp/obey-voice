package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// workNarrationMilestones is the voice_work_narration setting that speaks one
// line per closed fold row. The default, status_only, speaks nothing until the
// user asks what is happening.
const workNarrationMilestones = "milestones"

// spawnClarifier is what the assistant says instead of spawning on an assent
// that has no conversation behind it.
const spawnClarifier = "I did not catch what you would like me to work on. What should I do?"

// Agency wires spawn/abort/status routes to the async job table and worker brain.
type Agency struct {
	pipeline    *Pipeline
	jobs        *job.Table
	worker      brain.Provider
	cfg         *config.Config
	planFactory func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error)
	narrator    *Narrator
	// work folds a job's tool calls into rows: what "what's happening" answers
	// with, and what milestone narration speaks one line of.
	work *workfold.Tracker

	mu             sync.Mutex
	pending        []pendingDelivery
	workerReplies  []workerReply
	workerThreads  map[string][]brain.Turn
	workerOrigins  map[string]workerOrigin
	workerInitErr  error
	workMu         sync.RWMutex
	coordinator    *work.Coordinator
	workScope      work.WorkScope
	workObserver   func(work.Snapshot)
	jobScopes      map[string]work.WorkScope
	managed        map[*delegation.Runtime]struct{}
	managedJobs    map[string]*delegation.Runtime
	managedCurrent *delegation.Runtime
	managedClosing map[*delegation.Runtime]bool
	liveManaged    func() *delegation.Runtime // guarded by mu; provider accessor is concurrency-safe
	personas       map[string]bool            // guarded by mu
	// heldReplies wait for their speech to start before history may take
	// them, so the saved thread follows the order the user heard.
	heldReplies map[string]workerReply
}

// pendingDelivery is one completed job waiting to be spoken when the pipeline
// is idle. A slice, not a single string: queued jobs finish sequentially, but
// playback or a foreground turn can delay delivery, and a later completion
// must not overwrite an earlier one.
type pendingDelivery struct {
	JobID        string
	Text         string
	origin       *workerOrigin
	releaseVoice func()
	// result marks a job's answer, as opposed to a milestone line: starting
	// or giving up on its speech delivers the held reply.
	result *events.JobResultDelivered
}

// NewAgency constructs the spawn coordinator. worker may be nil when construction
// failed; spawn will surface the error at job start.
func NewAgency(p *Pipeline, jobs *job.Table, worker brain.Provider, cfg *config.Config, workerErr error) *Agency {
	a := &Agency{
		pipeline:       p,
		jobs:           jobs,
		worker:         worker,
		cfg:            cloneConfig(cfg),
		planFactory:    brain.NewPlannedWorker,
		work:           workfold.New(),
		workerThreads:  make(map[string][]brain.Turn),
		workerOrigins:  make(map[string]workerOrigin),
		workerInitErr:  workerErr,
		jobScopes:      make(map[string]work.WorkScope),
		managed:        make(map[*delegation.Runtime]struct{}),
		managedJobs:    make(map[string]*delegation.Runtime),
		managedClosing: make(map[*delegation.Runtime]bool),
	}
	if jobs != nil && tieredWorkers(cfg) {
		jobs.SetWorkerLimits(cfg.VoiceWorkers.Policy.ConcurrentWorkers, cfg.VoiceWorkers.Policy.QueuedWorkers)
	}
	a.narrator = NewNarrator(a)
	if jobs != nil && cfg != nil {
		jobs.SetEnabled(!cfg.VoiceWorkersDisabled)
	}
	a.notePersonaLocked(a.cfg)
	return a
}

// Wire attaches route handlers and job callbacks to the pipeline.
func (a *Agency) Wire() {
	if a == nil || a.pipeline == nil || a.jobs == nil {
		return
	}
	a.pipeline.Agency = a
	a.bindConversationWorkers(a.pipeline.Brain)
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
	if d.Kind != RouteSpawn {
		a.emitRouteDecision(d, "handled", "", "")
	}
	switch d.Kind {
	case RouteSpawn:
		a.handleSpawn(ctx, turn, metrics, d)
	case RouteNarrate, RouteNarratePause, RouteNarrateResume, RouteNarrateSkip, RouteNarrateStop:
		if a.workEnabled() && d.Kind == RouteNarrate && isLongStoryPhrase(d.Transcript) {
			a.pipeline.emit(events.Error{Stage: "job", Message: "Story-writing narration is unavailable with the experimental work inbox. Existing files can still be read aloud."})
			return
		}
		if a.narrator != nil {
			a.narrator.HandleRoute(ctx, turn, metrics, d)
		}
	case RouteJobAbort:
		a.handleAbort(ctx, turn, metrics)
	case RouteJobStatus:
		a.speakJobStatus(ctx, turn, metrics)
	}
	if a.pipeline.OnRoute != nil {
		a.pipeline.OnRoute(d)
	}
}

func (a *Agency) handleSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if err := a.jobs.CheckAdmission(); err != nil {
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	task := strings.TrimSpace(d.Transcript)
	history := a.liveHistory()

	// An assent with nothing behind it is not a task. Spawning on it hands a
	// worker with shell tools a prompt like "Yes." and lets it decide what
	// that meant — which is exactly how one went looking through the CLI's
	// session store and acted on somebody else's thread.
	if spawnLacksTask(history, task) {
		a.emitRouteDecision(d, "rejected", "", spawnClarifier)
		a.speakSpawnClarifier(ctx, turn, metrics)
		a.pipeline.emit(events.Info{Message: "spawn declined: no task in the utterance and no conversation behind it"})
		return
	}

	if task == "" {
		task = "background task"
	}
	experimental := a.workEnabled()
	worker, jobCfg, plan, workerErr := a.admitWorker(d.Tier)
	if workerErr != nil {
		a.emitRouteDecision(d, "rejected", "", workerErr.Error())
		a.pipeline.emit(events.Error{Stage: "job", Message: workerErr.Error()})
		return
	}
	if experimental && plan == nil && providerName(jobCfg) != "ollama" {
		a.emitRouteDecision(d, "rejected", "", "Experimental work requires a local worker; paid and unknown providers are disabled.")
		a.pipeline.emit(events.Error{Stage: "job", Message: "Experimental work requires a local worker; paid and unknown providers are disabled."})
		return
	}
	jobID := ""
	var key work.JobKey
	admission := frozenWorkerConfig(jobCfg, plan)
	if c, scope := a.workAuthority(); c != nil {
		jobID = newWorkID()
		var err error
		key, err = c.Register(scope, work.Admission{JobID: jobID, Task: task, Origin: "agency", Provider: providerName(jobCfg), Model: modelName(jobCfg), Config: admission})
		if err != nil {
			a.emitRouteDecision(d, "rejected", "", err.Error())
			a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
			return
		}
		a.workMu.Lock()
		a.jobScopes[jobID] = scope
		a.workMu.Unlock()
	}
	brief := spawnBrief(history, d.Transcript, briefPersonaID(metrics, jobCfg))
	run := a.workerRun(brief, worker, jobCfg)
	if plan != nil {
		run = a.plannedWorkerRun(brief, jobCfg, plan)
	}
	refs := chatmedia.Input(ctx)
	if len(refs) > 0 {
		originalRun := run
		run = func(jobCtx context.Context) (string, error) { return originalRun(chatmedia.WithInput(jobCtx, refs)) }
	}
	var runtimeBudget time.Duration
	if plan != nil {
		runtimeBudget = time.Duration(plan.Limits.RuntimeSeconds) * time.Second
	}
	origin := workerOrigin{Scope: a.pipeline.ConversationScope(), Trace: events.TraceFromContext(ctx), admission: admission}
	if metrics != nil {
		origin.Envelope = metrics.response
		origin.Trace = metrics.trace
	}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = a.pipeline.responseTTS(ctx)
	origin.releaseVoice = a.pipeline.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	acceptedID, err := a.jobs.Start(ctx, job.Spec{
		RuntimeBudget: runtimeBudget,
		ID:            jobID,
		OnAccepted: func(id string) {
			a.mu.Lock()
			a.workerOrigins[id] = origin
			a.mu.Unlock()
			a.emitRouteDecision(d, "accepted", id, "")
		},
		// Task is the label the user hears and sees; the brief is what the
		// worker reads. They were the same string, which is why the worker
		// only ever received one utterance.
		Task:     task,
		Provider: providerName(jobCfg),
		Model:    modelName(jobCfg),
		Run:      run,
	})
	if err != nil {
		origin.releaseVoice()
		a.emitRouteDecision(d, "rejected", jobID, err.Error())
		if c, _ := a.workAuthority(); c != nil && key.JobID != "" {
			_, _ = c.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeFailed, Text: truncateJobTask(err.Error()), Termination: "known"})
		}
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	// A routed turn must not silently end while its work continues. Speak only
	// after admission succeeded; the same bounded line is retained in canonical
	// history even for muted/text-only hosts. This does not wait for the worker.
	if turn != nil && metrics != nil {
		line := "I'll work on that and report back."
		for _, info := range a.jobs.Status() {
			if info.ID == acceptedID && info.State == job.StateQueued {
				line = "I've queued that task and will report back."
				break
			}
		}
		if err := a.pipeline.speakStandalone(ctx, turn, metrics, line); err != nil {
			metrics.degraded = true
		}
	}
}

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

// liveHistory is the talking brain's conversation, or nil when there is no
// brain to ask. A worker that gets nothing is handled by spawnLacksTask. A
// provider that sends a hosted service only its persona's share gives the
// worker only that share.
func (a *Agency) liveHistory() []brain.Turn {
	if a.pipeline == nil || a.pipeline.Brain == nil {
		return nil
	}
	if live := a.pipeline.Brain; brain.HostedSharedHistory(live) {
		return live.(brain.ContinuityTransferProvider).ContinuitySnapshot().Working
	}
	return a.pipeline.Brain.History()
}

// speakSpawnClarifier asks rather than guesses.
func (a *Agency) speakSpawnClarifier(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, spawnClarifier); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("clarifier speech failed: %v", err)})
	}
}

func (a *Agency) workerRun(brief string, worker brain.Provider, cfg *config.Config) func(context.Context) (string, error) {
	return a.workerRunWithBudget(brief, worker, cfg, legacyWorkerBudget(a.workEnabled(), cfg))
}

func (a *Agency) workerRunWithBudget(brief string, worker brain.Provider, cfg *config.Config, budget *brain.WorkerBudget) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		if worker == nil {
			return "", errors.New("worker provider is not configured")
		}
		worker = a.freshWorker(worker)
		if end, ok := job.BudgetEnd(ctx); ok {
			ctx = brain.WithWorkerDeadline(ctx, end)
		}
		if a.workEnabled() && !tieredWorkers(cfg) && providerName(cfg) == "ollama" {
			if validator, ok := worker.(brain.LegacyWorkerValidator); ok {
				if err := validator.ValidateLegacyWorker(ctx); err != nil {
					return "", fmt.Errorf("validate local worker: %w", err)
				}
			}
		} else if !tieredWorkers(cfg) {
			if validator, ok := worker.(brain.WorkerToolValidator); ok {
				if err := validator.ValidateWorkerTools(ctx); err != nil {
					return "", fmt.Errorf("validate worker tools: %w", err)
				}
			}
		}
		a.bindWorkerAdmission(worker)
		// Tiered admissions always run the full worker tool surface. The
		// conversation flag controls the live turn/router; it must not disable
		// tools for a worker that was explicitly admitted to a tier.
		tools := true
		if cfg != nil {
			tools = cfg.VoiceToolsEnabled || tieredWorkers(cfg)
		}
		refs := chatmedia.Input(ctx)
		if err := validateProviderImages(ctx, worker, refs); err != nil {
			return "", err
		}
		opts := brain.WorkerStreamOptions(tools)
		opts.Attachments = refs
		opts.WorkerBudget = budget
		opts.OnWorkerUsage = a.workerUsageHook(job.IDFromContext(ctx), budget)
		opts.OnToolStart, opts.OnToolEnd = a.foldingToolHooks(job.IDFromContext(ctx))
		id := job.IDFromContext(ctx)
		a.saveWorkerThread(id, []brain.Turn{{Role: "user", Content: brief, Attachments: refs}})
		start, end := opts.OnToolStart, opts.OnToolEnd
		opts.OnToolStart = func(call brain.ToolCall) { a.captureWorkerThread(id, worker); start(call) }
		opts.OnToolEnd = func(result brain.ToolResult) { end(result); a.captureWorkerThread(id, worker) }
		result, err := worker.ThinkFull(ctx, brief, opts)
		if err == nil {
			result, _ = finalizeProviderReply(worker, result, config.WorkDirFrom(cfg), func(message string) { a.pipeline.emit(events.Info{Message: message}) })
		}
		a.captureWorkerThread(id, worker)
		return result, err
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
	_, key, _ := a.callbackKey(jobID)
	start := func(call brain.ToolCall) {
		if a.workEnabled() && !a.acceptJobCallback(jobID) {
			return
		}
		event := toolStartedEvent(jobID, call)
		event.WorkScope = key.Scope
		a.pipeline.emit(event)
		if closed := a.work.Add(event); closed != nil {
			a.narrateRow(jobID, *closed)
		}
	}
	end := func(res brain.ToolResult) {
		if a.workEnabled() && !a.acceptJobCallback(jobID) {
			return
		}
		event := toolFinishedEvent(jobID, res)
		event.WorkScope = key.Scope
		a.pipeline.emit(event)
		a.work.Finish(event)
	}
	return start, end
}

// narrateRow speaks one short line for a row that just closed, under the same
// idle gate as a job summary. Silent unless voice_work_narration is milestones.
func (a *Agency) narrateRow(jobID string, row workfold.Row) {
	if a.workEnabled() {
		return
	}
	cfg := a.currentConfig()
	if cfg == nil || cfg.VoiceWorkNarration != workNarrationMilestones {
		return
	}
	a.enqueueSummary(jobID, capitalize(row.String())+".")
	a.tryFlushPending()
}

// closeFold ends the job's fold, speaking its last row under milestones.
// narrate is false for an abort: the cancel line or the budget line is all
// an abort says.
func (a *Agency) closeFold(jobID string, narrate bool) {
	rows := a.work.Seal(jobID)
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
	experimental := a.workEnabled()
	a.mu.Lock()
	defer a.mu.Unlock()
	if experimental && providerName(a.cfg) != "ollama" {
		return nil, errors.New("experimental work requires a local worker")
	}
	if a.worker != nil {
		return a.worker, nil
	}
	if a.workerInitErr != nil {
		return nil, a.workerInitErr
	}
	return nil, fmt.Errorf("worker provider is not configured")
}

func (a *Agency) currentConfig() *config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneConfig(a.cfg)
}

func (a *Agency) workerAdmission() (brain.Provider, *config.Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.workerInitErr
	if a.worker == nil && err == nil {
		err = errors.New("worker provider is not configured")
	}
	return a.worker, cloneConfig(a.cfg), err
}

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

// queueResult queues a finished job's answer for history and idle speech and
// returns its JobFinished. An answer that will be spoken is held: the chat
// and history take it when its speech starts, or when that speech is given up.
func (a *Agency) queueResult(id, task, result string, refs []chatmedia.Ref, elapsed time.Duration) events.JobFinished {
	a.mu.Lock()
	origin, ok := a.workerOrigins[id]
	a.mu.Unlock()
	primary, _ := a.pipeline.ttsProviders()
	if ok {
		primary = origin.Voices.primary
	}
	finishedEnvelope, deliveredEnvelope := origin.Envelope, origin.Envelope
	finished := events.JobFinished{ID: id, Task: task, Summary: result, Attachments: refs, Elapsed: elapsed, Envelope: &finishedEnvelope, Scope: origin.Scope}
	finished.SpeechPending = a.pipeline.canSpeak(primary)
	a.queueWorkerReply(id, result, finished.SpeechPending)
	var delivered *events.JobResultDelivered
	if finished.SpeechPending {
		delivered = &events.JobResultDelivered{ID: id, Task: task, Summary: result, Attachments: refs, Envelope: &deliveredEnvelope, Scope: origin.Scope}
	}
	a.enqueueSpeech(id, a.cleanSummary(result), delivered)
	return finished
}

// deliverResult releases a held answer once: the chat shows it and history
// may take it. Later calls for the same job do nothing.
func (a *Agency) deliverResult(d pendingDelivery, spoken bool) {
	if d.result == nil {
		return
	}
	a.mu.Lock()
	reply, held := a.heldReplies[d.JobID]
	if held {
		delete(a.heldReplies, d.JobID)
		if strings.TrimSpace(reply.Text) != "" {
			a.workerReplies = append(a.workerReplies, reply)
		}
	}
	a.mu.Unlock()
	if !held {
		return
	}
	event := *d.result
	event.Spoken = spoken
	a.emitJobTrace(d.JobID, event)
}

// heldResultWaiting reports a held answer that an idle flush would speak now.
func (a *Agency) heldResultWaiting() bool {
	if a == nil || a.workEnabled() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canSpeakIdleSummary() && slices.ContainsFunc(a.pending, func(d pendingDelivery) bool { return d.result != nil })
}

func (a *Agency) enqueueSummary(id, text string) { a.enqueueSpeech(id, text, nil) }

func (a *Agency) enqueueSpeech(id, text string, result *events.JobResultDelivered) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "That finished with nothing to report."
	}
	a.mu.Lock()
	next := pendingDelivery{JobID: id, Text: text, result: result}
	if origin, ok := a.workerOrigins[id]; ok {
		next.origin = &origin
		next.releaseVoice = a.pipeline.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	}
	a.pending = append(a.pending, next)
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
	if a.workEnabled() {
		return
	}
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
		ctx := context.Background()
		var metrics *turnMetrics
		if origin := next.origin; origin != nil {
			if origin.Scope.Valid() && (origin.Scope != a.pipeline.ConversationScope() || a.pipeline.ConversationRetired()) {
				next.releaseVoice()
				a.deliverResult(next, false)
				continue
			}
			ctx = context.WithValue(events.WithResponse(ctx, origin.Envelope), responseVoicesKey{}, origin.Voices)
			metrics = newTurnMetrics()
			metrics.response = origin.Envelope
			metrics.ttsEngine = ttsIdentityFor(origin.Voices.primary)
		}
		if next.result != nil {
			if metrics == nil {
				metrics = newTurnMetrics()
			}
			metrics.onAudible = func() { a.deliverResult(next, true) }
		}
		err := a.pipeline.speakStandalone(ctx, nil, metrics, next.Text)
		if next.releaseVoice != nil {
			next.releaseVoice()
		}
		a.deliverResult(next, false)
		if err != nil {
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
