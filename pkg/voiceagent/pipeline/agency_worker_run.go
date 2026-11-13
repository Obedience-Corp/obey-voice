package pipeline

import (
	"context"
	"errors"
	"fmt"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// workNarrationMilestones is the voice_work_narration setting that speaks one
// line per closed fold row. The default, status_only, speaks nothing until the
// user asks what is happening.
const workNarrationMilestones = "milestones"

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
