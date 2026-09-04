package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

const defaultSpawnAck = "Working on it."

// Agency wires spawn/abort/status routes to the async job table and worker brain.
type Agency struct {
	pipeline *Pipeline
	jobs     *job.Table
	worker   brain.Provider
	cfg      *config.Config
	ack      string
	narrator *Narrator

	mu              sync.Mutex
	pendingSummary  string
	pendingFailure  string
	workerInitErr   error
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
	if a.narrator != nil {
		a.narrator.Wire()
	}
}

// HandleRoute executes spawn, job.abort, and job.status routes.
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
		} else if a.pipeline.OnRoute != nil {
			a.pipeline.OnRoute(d)
		}
	case RouteJobAbort:
		a.handleAbort(d)
	case RouteJobStatus:
		a.speakJobStatus(ctx, turn, metrics)
	default:
		if a.pipeline.OnRoute != nil {
			a.pipeline.OnRoute(d)
		}
	}
}

func (a *Agency) handleSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	task := strings.TrimSpace(d.Transcript)
	if task == "" {
		task = "background task"
	}
	a.speakAck(ctx, turn, metrics)
	id, err := a.jobs.Start(ctx, job.Spec{
		Task: task,
		Run:  a.workerRun(task),
	})
	if err != nil {
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	a.pipeline.emit(events.JobStarted{ID: id, Task: task})
	if a.pipeline.OnRoute != nil {
		a.pipeline.OnRoute(d)
	}
}

func (a *Agency) workerRun(task string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		worker, err := a.workerProvider()
		if err != nil {
			return "", err
		}
		tools := true
		if a.cfg != nil {
			tools = a.cfg.VoiceToolsEnabled
		}
		return worker.ThinkFull(ctx, task, brain.WorkerStreamOptions(tools))
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

func (a *Agency) handleAbort(d RouteDecision) {
	st := a.jobs.Status()
	for _, info := range st {
		if info.State == job.StateRunning || info.State == job.StateQueued {
			a.jobs.Abort(info.ID)
		}
	}
	if a.pipeline.OnRoute != nil {
		a.pipeline.OnRoute(d)
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
	for _, info := range a.jobs.Status() {
		if info.ID == id {
			task = info.Task
			break
		}
	}
	switch state {
	case job.StateDone:
		a.pipeline.emit(events.JobFinished{ID: id, Task: task, Summary: result})
		a.enqueueSummary(a.cleanSummary(result))
	case job.StateFailed:
		msg := "That did not work."
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			msg = truncateJobTask(err.Error())
		}
		a.pipeline.emit(events.JobFailed{ID: id, Task: task, Err: msg})
		a.enqueueSummary(msg)
	case job.StateAborted:
		// abort is silent unless the user asked for status.
	}
	a.tryFlushPending()
}

func (a *Agency) enqueueSummary(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "That finished with nothing to report."
	}
	a.mu.Lock()
	a.pendingSummary = text
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
	a.mu.Lock()
	text := a.pendingSummary
	a.pendingSummary = ""
	a.mu.Unlock()
	if text == "" {
		return
	}
	if !a.canSpeakIdleSummary() {
		a.mu.Lock()
		if a.pendingSummary == "" {
			a.pendingSummary = text
		}
		a.mu.Unlock()
		return
	}
	a.pipeline.speakStandalone(context.Background(), nil, nil, text)
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
