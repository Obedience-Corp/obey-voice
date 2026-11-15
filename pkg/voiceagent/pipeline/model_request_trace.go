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
		status := "completed"
		switch {
		case errors.Is(r.Err, context.Canceled):
			status = "cancelled"
		case r.Err != nil:
			status = "failed"
		}
		p.traceModelRequest(trace, id, "end", status, req.Sent, r.Finished, ended)
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
