package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

const defaultSpawnAck = "Working on it."

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

// onJobStarted runs inside Table.Start before the worker can begin, so
// JobStarted always precedes the job's tool and terminal events.
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
		tools := true
		if a.cfg != nil {
			tools = a.cfg.VoiceToolsEnabled
		}
		opts := brain.WorkerStreamOptions(tools)
		opts.OnToolStart, opts.OnToolEnd = a.pipeline.jobToolHooks(job.IDFromContext(ctx))
		return worker.ThinkFull(ctx, brief, opts)
	}
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
	var active *job.Info
	for _, info := range a.jobs.Status() {
		if info.State == job.StateRunning || info.State == job.StateQueued {
			copy := info
			active = &copy
			break
		}
	}
	line := "No job is running."
	if active != nil {
		switch active.State {
		case job.StateQueued:
			line = fmt.Sprintf("Queued: %s.", truncateJobTask(active.Task))
		default:
			line = fmt.Sprintf("Still working on %s.", truncateJobTask(active.Task))
		}
	}
	a.pipeline.speakStandalone(ctx, turn, metrics, line)
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
	task := ""
	var elapsed time.Duration
	for _, info := range a.jobs.Status() {
		if info.ID == id {
			task = info.Task
			elapsed = info.Elapsed()
			break
		}
	}
	// Enqueue before emitting: the terminal event is the signal that the job's
	// bookkeeping is complete, and a consumer may read pending on seeing it.
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
		// Visible on the bus; still silent in speech unless the user asks.
		a.pipeline.emit(events.JobAborted{ID: id, Task: task, Elapsed: elapsed})
	}
	a.tryFlushPending()
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
