package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// ObserveModelRequests traces each request the brain sends to its model. A
// host binds it before starting the brain's warmup, which runs outside any turn.
func (p *Pipeline) ObserveModelRequests() {
	if o, ok := p.Brain.(interface {
		SetModelRequestObserver(brain.ModelRequestObserver)
	}); ok {
		o.SetModelRequestObserver(p.observeModelRequest)
	}
}

// observeModelRequest emits a model_request span under the caller's turn, or
// with runtime scope and no turn when the request belongs to none.
func (p *Pipeline) observeModelRequest(ctx context.Context, req brain.ModelRequest) func(brain.ModelRequestResult) {
	trace := events.TraceFromContext(ctx)
	if trace.RuntimeID == "" {
		trace = p.sourceTrace(req.Sent)
	}
	prefix := trace.RuntimeID
	if trace.TurnID != nil {
		prefix = *trace.TurnID
	}
	id := prefix + ":model_request:" + newWorkID()
	started := events.TraceModelRequest{Purpose: req.Purpose, Provider: req.Provider, Model: req.Model}
	p.traceModelRequest(trace, id, "start", "running", req.Sent, time.Time{}, started)
	return func(r brain.ModelRequestResult) {
		ended := started
		ended.FirstTokenMS = traceOffset(trace, r.FirstChunk)
		ended.PromptTokens, ended.OutputTokens = r.PromptTokens, r.OutputTokens
		ended.LoadMS, ended.PrefillMS, ended.GenerationMS = durationMS(r.Load), durationMS(r.Prefill), durationMS(r.Generation)
		p.traceModelRequest(trace, id, "end", modelRequestStatus(ctx, r.Err), req.Sent, r.Finished, ended)
	}
}

// modelRequestStatus reports cancelled only when the request's context was
// cancelled for a reason other than a deadline. The turn's idle watchdog
// cancels with a DeadlineExceeded cause, which is a timeout, so it fails.
// Every other error fails, including the engine's own stops from inside the
// response callback.
func modelRequestStatus(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return "failed"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "failed"
	}
}

func (p *Pipeline) traceModelRequest(trace events.TraceContext, id, state, status string, start, end time.Time, request events.TraceModelRequest) {
	if trace.RuntimeID == "" {
		return
	}
	trace = p.stampTrace(trace)
	p.emit(events.PipelineTrace{Trace: &trace, Span: events.TraceSpan{
		ID: id, ParentID: trace.TurnID, Stage: "model_request", Lane: "model",
		State: state, Status: status, StartMS: traceOffset(trace, start), EndMS: traceOffset(trace, end),
		Request: &request,
	}})
}

func durationMS(d *time.Duration) *float64 {
	if d == nil {
		return nil
	}
	return new(float64(*d) / float64(time.Millisecond))
}
