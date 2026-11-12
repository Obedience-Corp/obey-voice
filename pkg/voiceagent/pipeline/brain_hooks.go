package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func (p *Pipeline) emit(event events.Event) {
	if events.TraceOf(event) == nil {
		trace := p.sourceTrace(time.Now())
		id := events.SourceJobID(event)
		if id != "" {
			if p.Agency != nil {
				if frozen := p.Agency.jobTrace(id); frozen.RuntimeID != "" {
					trace = frozen
				}
			}
			trace.TurnID = nil
			trace.TurnStartMS = nil
			trace.ResponseID = nil
			trace.PersonaID = nil
			trace.JobID = &id
		}
		if runtime, session, epoch, ok := events.SourceWorkScope(event); ok {
			if runtime != trace.RuntimeID {
				trace = events.TraceContext{RuntimeID: runtime, SessionID: session, ConversationEpoch: epoch}
			} else {
				trace.SessionID = session
				trace.ConversationEpoch = epoch
			}
		}
		if id != "" {
			trace.JobID = &id
		}
		event = events.AttributeTrace(event, p.stampTrace(trace))
	}
	tracePipelineEvent(event)
	if p.Events != nil {
		p.Events.Emit(event)
	}
}

func (p *Pipeline) bindBrainInfo() {
	primary, _ := p.ttsProviders()
	if s, ok := primary.(interface {
		SetProviderStatusSink(func(context.Context, events.Info))
	}); ok {
		s.SetProviderStatusSink(func(ctx context.Context, info events.Info) { p.emitResponseContext(ctx, info) })
	}
	if is, ok := p.Brain.(interface{ SetInfoSink(func(events.Info)) }); ok {
		is.SetInfoSink(func(info events.Info) { p.emit(info) })
	}
	if cs, ok := p.Brain.(interface{ SetContinuitySink(func(events.Event)) }); ok {
		cs.SetContinuitySink(func(e events.Event) { p.emit(e) })
	}
}

// toolStartHook emits ToolCallStarted for UI (stdout + TUI).
func (p *Pipeline) toolStartHook(contexts ...context.Context) func(brain.ToolCall) {
	return func(call brain.ToolCall) {
		p.bumpBrainDeadline()
		e := toolStartedEvent("", call)
		if len(contexts) > 0 {
			ctx := contexts[0]
			trace := events.TraceFromContext(ctx)
			now := time.Now()
			p.traceSpan(trace, traceID(trace, "tool:"+call.ID), "tool", "start", "running", now, time.Time{}, nil, nil)
			p.emitResponseContext(ctx, e)
		} else {
			p.emit(e)
		}
	}
}

func (p *Pipeline) toolStartHookTracked(toolsFired *bool, contexts ...context.Context) func(brain.ToolCall) {
	base := p.toolStartHook(contexts...)
	return func(call brain.ToolCall) {
		if toolsFired != nil {
			*toolsFired = true
		}
		base(call)
	}
}

// jobToolHooks attributes a background worker's tool calls to jobID. Unlike the
// live hooks it never bumps the brain deadline: a worker tool must not extend
// the watchdog on the conversation the user is having.
func (p *Pipeline) jobToolHooks(jobID string, scopes ...work.WorkScope) (func(brain.ToolCall), func(brain.ToolResult)) {
	start := func(call brain.ToolCall) { p.emit(toolStartedEvent(jobID, call, scopes...)) }
	end := func(res brain.ToolResult) { p.emit(toolFinishedEvent(jobID, res, scopes...)) }
	return start, end
}

func toolStartedEvent(jobID string, call brain.ToolCall, scopes ...work.WorkScope) events.ToolCallStarted {
	e := events.ToolCallStarted{
		ID:      call.ID,
		JobID:   jobID,
		Name:    call.Name,
		Kind:    string(call.Kind),
		Summary: call.Summary,
	}
	if len(scopes) > 0 {
		e.WorkScope = scopes[0]
	}
	return e
}

func toolFinishedEvent(jobID string, res brain.ToolResult, scopes ...work.WorkScope) events.ToolCallFinished {
	e := events.ToolCallFinished{
		ID:      res.ID,
		JobID:   jobID,
		Name:    res.Name,
		Kind:    string(res.Kind),
		Preview: res.Preview,
		Err:     res.Err,
		Elapsed: res.Elapsed,
		Outcome: res.Outcome,
	}
	if len(scopes) > 0 {
		e.WorkScope = scopes[0]
	}
	return e
}

func (p *Pipeline) liveRouterToolsPolicy() brain.RouterToolsPolicy {
	if p.Agency != nil && tieredWorkers(p.Agency.currentConfig()) {
		return brain.RouterToolsRouteOnly
	}
	if !p.VoiceToolsEnabled {
		return ""
	}
	if p.RouterToolsPolicy != "" {
		return p.RouterToolsPolicy
	}
	return brain.RouterToolsRouteOnly
}

func (p *Pipeline) liveRouteJSON() bool {
	return p.RouteJSON && p.liveRouterToolsPolicy() == brain.RouterToolsRouteOnly
}

func (p *Pipeline) routeContext() RouteContext {
	ctx := RouteContext{}
	if p.NarrationActive != nil {
		ctx.NarrationActive = p.NarrationActive()
	}
	if p.JobActive != nil {
		ctx.JobActive = p.JobActive()
	}
	if p.Agency != nil {
		ctx.TieredWorkers = tieredWorkers(p.Agency.currentConfig())
	}
	return ctx
}

// routedSpeechContext admits the routed acknowledgement after the turn stopped
// its own pre-route speech, which invalidated the turn's playback binding. A
// stop the user asked for stays fenced: it is checked after the fresh bind so
// a stop racing this call still invalidates whichever binding is returned.
// The stale ctx returned after a user stop is refused with context.Canceled,
// which speakStandalone reports as an Info line rather than a playback error.
func (p *Pipeline) routedSpeechContext(ctx context.Context, metrics *turnMetrics) context.Context {
	if p.Player == nil || ctx.Err() != nil {
		return ctx
	}
	fresh := audio.RebindPlaybackContext(ctx, p.Player)
	if metrics != nil && metrics.speechStopped() {
		return ctx
	}
	return fresh
}

func (p *Pipeline) completeRoutedTurn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, transcript string, kind RouteKind, source string) error {
	decision := RouteDecision{Kind: kind, Transcript: transcript, Source: source}
	if metrics != nil {
		decision.Tier, decision.ReasonCode = metrics.route.Tier, metrics.route.ReasonCode
	}
	metrics.recording.takeRoute()
	var replyParts []string
	metrics.routedSpeech = &replyParts
	if p.Agency != nil {
		p.Agency.HandleRoute(ctx, turn, metrics, decision)
	} else if p.OnRoute != nil {
		p.OnRoute(decision)
	}
	metrics.routedSpeech = nil
	response := strings.Join(replyParts, " ")
	err := p.appendRoutedHistory(metrics, transcript, response)
	if err != nil {
		metrics.degraded = true
		p.emit(events.Error{Stage: "history", Message: err.Error()})
	}
	p.emitResponse(metrics, events.ResponseReady{
		Response:    response,
		Interrupted: ctx.Err() != nil,
		Degraded:    metrics.degraded,
	})
	if err != nil {
		turn.finish(TurnFailed)
	} else if ctx.Err() != nil {
		turn.finish(TurnInterrupted)
		err = ctx.Err()
	} else {
		turn.finish(TurnCompleted)
	}
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return err
}

// toolEndHook emits ToolCallFinished for UI.
func (p *Pipeline) toolEndHook(contexts ...context.Context) func(brain.ToolResult) {
	return func(res brain.ToolResult) {
		p.bumpBrainDeadline()
		e := toolFinishedEvent("", res)
		if len(contexts) > 0 {
			ctx := contexts[0]
			trace := events.TraceFromContext(ctx)
			end := time.Now()
			status := string(e.OutcomeStatus())
			p.traceSpan(trace, traceID(trace, "tool:"+res.ID), "tool", "end", status, end.Add(-e.ResultElapsed()), end, nil, nil)
			p.emitResponseContext(ctx, e)
		} else {
			p.emit(e)
		}
	}
}

// usageHook emits per-request token accounting so prefix-cache regressions
// are visible in the activity feed, not vibes.
func (p *Pipeline) usageHook() func(prefill, gen int) {
	return func(prefill, gen int) {
		p.emit(events.TokenUsage{Prefill: prefill, Gen: gen})
	}
}

// sessionWarnHook emits SessionWarning so a growing harness session is visible
// in the activity feed before it becomes a latency or cost problem.
// promptWarnHook surfaces a non-fatal prompt-resolution failure. The turn keeps
// speaking on its last good prompt, so this is reported as an error event for
// visibility rather than failing the turn — a half-saved persona file must not
// end a live conversation, but it must not be silent either, or the user is left
// wondering why their edit did nothing.
func (p *Pipeline) promptWarnHook() func(message string) {
	return func(message string) {
		p.emit(events.Error{Stage: "prompt", Message: message})
	}
}

func (p *Pipeline) sessionWarnHook() func(promptTokens, threshold int) {
	return func(promptTokens, threshold int) {
		p.emit(events.SessionWarning{PromptTokens: promptTokens, Threshold: threshold})
	}
}
