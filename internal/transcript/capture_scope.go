package transcript

import (
	"math"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Scope exclusions are not queue loss: a successor recording must not adopt an
// explicitly different runtime/session/epoch. Unknown legacy scope stays unknown.
func captureScopeConflict(expected events.TraceContext, event events.Event) bool {
	conflict := func(runtime, session, epoch string) bool {
		for _, pair := range [][2]string{{expected.RuntimeID, runtime}, {expected.SessionID, session}, {expected.ConversationEpoch, epoch}} {
			if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
				return true
			}
		}
		return false
	}
	if trace := events.TraceOf(event); trace != nil && conflict(trace.RuntimeID, trace.SessionID, trace.ConversationEpoch) {
		return true
	}
	runtime, session, epoch, ok := events.SourceWorkScope(event)
	return ok && conflict(runtime, session, epoch)
}

// Oversize diagnostics cannot retain the original oversized/mutable trace. An
// oversized identity becomes explicitly unavailable instead of being truncated
// into an invented identifier. Numeric unknown/invalid values remain null.
func cloneDiagnosticTrace(trace *events.TraceContext) *events.TraceContext {
	if trace == nil {
		return nil
	}
	bounded := func(value string) string {
		if len(value) > 256 {
			return ""
		}
		return strings.Clone(value)
	}
	id := func(value *string) *string {
		if value == nil || len(*value) > 256 {
			return nil
		}
		v := strings.Clone(*value)
		return &v
	}
	number := func(value *float64) *float64 {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return nil
		}
		v := *value
		return &v
	}
	return &events.TraceContext{CaptureID: bounded(trace.CaptureID), RuntimeID: bounded(trace.RuntimeID), SessionID: bounded(trace.SessionID), ConversationEpoch: bounded(trace.ConversationEpoch), MonoMS: number(trace.MonoMS), TurnStartMS: number(trace.TurnStartMS), TurnID: id(trace.TurnID), ParentTurnID: id(trace.ParentTurnID), JobID: id(trace.JobID), ResponseID: id(trace.ResponseID), PersonaID: id(trace.PersonaID)}
}
