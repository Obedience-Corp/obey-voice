package pipeline

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"time"
)

// Runtime origin is retained by the pipeline, independent of work mode/viewers.
func (p *Pipeline) sourceTrace(at time.Time) events.TraceContext {
	p.identityMu.Lock()
	scope := p.identityScope
	if p.traceOrigin.IsZero() {
		p.traceOrigin = at
	}
	origin := p.traceOrigin
	p.identityMu.Unlock()
	ms := float64(at.Sub(origin)) / float64(time.Millisecond)
	return events.TraceContext{Origin: origin, RuntimeID: scope.RuntimeEpoch, SessionID: scope.SessionID, ConversationEpoch: scope.ConversationEpoch, MonoMS: &ms}
}
func (p *Pipeline) stampTrace(trace events.TraceContext) events.TraceContext {
	origin := trace.Origin
	if !origin.IsZero() {
		ms := float64(time.Since(origin)) / float64(time.Millisecond)
		trace.MonoMS = &ms
	}
	return trace
}
func responseTrace(trace events.TraceContext, e events.ResponseEnvelope) events.TraceContext {
	if e.ResponseID != "" {
		id := e.ResponseID
		persona := e.PersonaID
		trace.ResponseID = &id
		trace.PersonaID = &persona
	}
	return trace
}

func (a *Agency) emitJobTrace(id string, event events.Event) {
	a.mu.Lock()
	origin := a.workerOrigins[id]
	a.mu.Unlock()
	trace := origin.Trace
	// Job identity is separate from its causal foreground invocation.
	trace.TurnID = nil
	trace.ParentTurnID = origin.Trace.TurnID
	trace.ResponseID = nil
	trace.PersonaID = nil
	trace.TurnStartMS = nil
	if trace.RuntimeID != "" {
		event = events.AttributeTrace(event, a.pipeline.stampTrace(trace))
	}
	a.pipeline.emit(event)
}

// CaptureTrace snapshots the runtime clock and session lane for recording headers.
func (p *Pipeline) CaptureTrace() events.TraceContext { return p.sourceTrace(time.Now()) }
