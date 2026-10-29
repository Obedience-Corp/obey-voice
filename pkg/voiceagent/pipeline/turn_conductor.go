package pipeline

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"time"
)

// turnConductor bridges the turn state machine into the live pipeline: stages
// report state transitions and it guarantees exactly one terminal TurnMetrics
// emission per turn, regardless of which return path ends the turn.
type turnConductor struct {
	p        *Pipeline
	machine  *turnMachine
	metrics  *turnMetrics
	finished bool
}

func (p *Pipeline) newTurnConductor(metrics *turnMetrics, parents ...context.Context) *turnConductor {
	metrics.trace = responseTrace(p.sourceTrace(metrics.start), metrics.response)
	id := newWorkID()
	metrics.trace.TurnID = &id
	metrics.trace.TurnStartMS = metrics.trace.MonoMS
	if len(parents) > 0 {
		metrics.trace.ParentTurnID = events.TraceFromContext(parents[0]).TurnID
	}
	p.traceSpan(metrics.trace, id, "turn", "start", "running", metrics.start, time.Time{}, nil, nil)
	return &turnConductor{p: p, machine: newTurnMachine(), metrics: metrics}
}

// to advances the lifecycle state, ignoring illegal or out-of-order transitions
// so a late stage signal cannot corrupt the turn outcome.
func (c *turnConductor) to(state TurnState) { c.machine.To(state) }

// finish moves the machine to its terminal state and emits the single terminal
// metrics event. Later calls are no-ops, so every return path can call it.
func (c *turnConductor) finish(terminal TurnState) {
	if c.finished {
		return
	}
	c.finished = true
	c.machine.To(terminal)
	// The machine is the outcome authority: the emitted metrics carry the
	// terminal state it settled on. A rejected transition (late or illegal
	// signal) falls back to the caller's intent so the outcome is never a
	// non-terminal state.
	outcome, ok := c.machine.Terminal()
	if !ok {
		outcome = terminal
	}
	m := c.metrics.snapshot()
	m.Outcome = outcome.String()
	c.p.traceSpan(c.metrics.trace, *c.metrics.trace.TurnID, "turn", "end", m.Outcome, c.metrics.start, time.Now(), nil, nil)
	if !c.metrics.modelRequest.IsZero() {
		status := c.metrics.modelOutcome
		if status == "" {
			status = m.Outcome
		}
		if !c.metrics.modelComplete.IsZero() && c.metrics.modelOutcome == "" {
			status = "completed"
		}
		c.p.traceSpan(c.metrics.trace, traceID(c.metrics.trace, "model"), "model", "end", status, c.metrics.modelRequest, c.metrics.modelComplete, nil, nil)
	}
	c.p.emitResponse(c.metrics, m)
	c.p.warnIfSynthesisCannotKeepUp(m)
}
