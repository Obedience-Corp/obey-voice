package brain

import (
	"strings"

	"github.com/ollama/ollama/api"
)

// TrimContinuity makes room for an incoming turn after automatic summarization
// failed. Only the working projection is reduced; the canonical transcript and
// archived exchanges remain available. No current user input has been appended.
func (o *OllamaBrain) TrimContinuity(input string) {
	if o.cfg == nil || o.effectiveNumCtx() <= 0 {
		return
	}
	ratio := o.cfg.ContinuitySoftLimitRatio
	if ratio <= 0 || ratio > .9 {
		ratio = .8
	}
	limit := int(float64(o.effectiveNumCtx())*ratio) - (len(input)+19)/4 - 1
	var dropped []api.Message
	var droppedTurns []Turn
	for len(o.history) > 0 && o.estimateTokens("") > limit {
		// Remove the oldest complete exchange, including its tool calls and
		// results: everything before the second user message.
		start := len(o.history)
		for i := 1; i < len(o.history); i++ {
			if o.history[i].Role == "user" {
				start = i
				break
			}
		}
		dropped = append(dropped, o.history[:start]...)
		droppedTurns = append(droppedTurns, o.working[:min(start, len(o.working))]...)
		o.history = append([]api.Message(nil), o.history[start:]...)
		if start < len(o.working) {
			o.working = deepCopyTurns(o.working[start:])
		} else {
			o.working = nil
		}
	}
	o.archiveTurns(dropped, droppedTurns)
	o.maybeEmitContinuityBudget(input)
}

// PrepareCompact counts the upcoming briefing and its per-message overhead
// before the provider appends it. The real request is budget-checked again.
func (o *OllamaBrain) PrepareCompact(prompt string) {
	o.ensureContextBudget(prompt + strings.Repeat(" ", 16))
	o.maybeEmitContinuityBudget("")
}
