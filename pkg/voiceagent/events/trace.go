package events

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"time"
)

const TraceSchema = "obey-voice.trace/v1"

// TraceContext is a source snapshot. Nil identifiers and boundaries mean unknown.
// Each emission copies the value; producers must never mutate an emitted pointer.
type TraceContext struct {
	Origin            time.Time `json:"-"`
	CaptureID         string    `json:"capture_id"`
	RuntimeID         string    `json:"runtime_id"`
	SessionID         string    `json:"session_id"`
	ConversationEpoch string    `json:"conversation_epoch"`
	MonoMS            *float64  `json:"mono_ms"`
	TurnID            *string   `json:"turn_id"`
	JobID             *string   `json:"job_id"`
	ParentTurnID      *string   `json:"parent_turn_id"`
	TurnStartMS       *float64  `json:"turn_start_ms"`
	ResponseID        *string   `json:"response_id"`
	PersonaID         *string   `json:"persona_id"`
}

// PipelineTrace is a low-rate source observation, never a viewer reconstruction.
type PipelineTrace struct {
	Trace *TraceContext `json:"trace"`
	Span  TraceSpan     `json:"span"`
}

func (PipelineTrace) eventType() string { return "pipeline_trace" }

type TraceSpan struct {
	ID           string   `json:"id"`
	ParentID     *string  `json:"parent_id"`
	Stage        string   `json:"stage"`
	Lane         string   `json:"lane"`
	SegmentIndex *int     `json:"segment_index"`
	AttemptID    *string  `json:"attempt_id"`
	State        string   `json:"state"`
	StartMS      *float64 `json:"start_ms"`
	EndMS        *float64 `json:"end_ms"`
	Status       string   `json:"status"`
}

type traceKey struct{}

func WithTrace(ctx context.Context, trace TraceContext) context.Context {
	return context.WithValue(ctx, traceKey{}, trace)
}
func TraceFromContext(ctx context.Context) TraceContext {
	t, _ := ctx.Value(traceKey{}).(TraceContext)
	return t
}

func TraceOf(event Event) *TraceContext {
	switch e := event.(type) {
	case ConversationCompacted:
		return e.Trace
	case DecisionEvaluated:
		return e.Trace
	case Error:
		return e.Trace
	case GeneratingVoice:
		return e.Trace
	case Info:
		return e.Trace
	case JobAborted:
		return e.Trace
	case JobFailed:
		return e.Trace
	case JobFinished:
		return e.Trace
	case JobQueued:
		return e.Trace
	case JobStarted:
		return e.Trace
	case ResponseDelta:
		return e.Trace
	case ResponseReady:
		return e.Trace
	case ResponseSpeakableStarted:
		return e.Trace
	case ResponseStreamingStarted:
		return e.Trace
	case RouteDecided:
		return e.Trace
	case STTPhase:
		return e.Trace
	case SessionWarning:
		return e.Trace
	case SpeakingComplete:
		return e.Trace
	case SpeakingInterrupted:
		return e.Trace
	case SpeakingStarted:
		return e.Trace
	case SpeechDiagnostic:
		return e.Trace
	case SpeechSegmentReady:
		return e.Trace
	case ThinkingComplete:
		return e.Trace
	case ThinkingStarted:
		return e.Trace
	case TokenUsage:
		return e.Trace
	case ToolCallFinished:
		return e.Trace
	case ToolCallStarted:
		return e.Trace
	case TurnInterrupted:
		return e.Trace
	case TurnMetrics:
		return e.Trace
	case UserInput:
		return e.Trace
	case VoiceGateStripped:
		return e.Trace
	case VoiceGenerated:
		return e.Trace
	case WorkDelivery:
		return e.Trace
	case WorkStateChanged:
		return e.Trace
	case WorkerChargeState:
		return e.Trace
	case WorkerLimitReached:
		return e.Trace
	case WorkerUsageRecorded:
		return e.Trace
	case PipelineTrace:
		return e.Trace
	}
	return nil
}

// AttributeTrace preserves concrete event types for existing typed subscribers.
func AttributeTrace(event Event, trace TraceContext) Event {
	switch e := event.(type) {
	case ConversationCompacted:
		e.Trace = &trace
		return e
	case DecisionEvaluated:
		e.Trace = &trace
		return e
	case Error:
		e.Trace = &trace
		return e
	case GeneratingVoice:
		e.Trace = &trace
		return e
	case Info:
		e.Trace = &trace
		return e
	case JobAborted:
		e.Trace = &trace
		return e
	case JobFailed:
		e.Trace = &trace
		return e
	case JobFinished:
		e.Trace = &trace
		return e
	case JobQueued:
		e.Trace = &trace
		return e
	case JobStarted:
		e.Trace = &trace
		return e
	case ResponseDelta:
		e.Trace = &trace
		return e
	case ResponseReady:
		e.Trace = &trace
		return e
	case ResponseSpeakableStarted:
		e.Trace = &trace
		return e
	case ResponseStreamingStarted:
		e.Trace = &trace
		return e
	case RouteDecided:
		e.Trace = &trace
		return e
	case STTPhase:
		e.Trace = &trace
		return e
	case SessionWarning:
		e.Trace = &trace
		return e
	case SpeakingComplete:
		e.Trace = &trace
		return e
	case SpeakingInterrupted:
		e.Trace = &trace
		return e
	case SpeakingStarted:
		e.Trace = &trace
		return e
	case SpeechDiagnostic:
		e.Trace = &trace
		return e
	case SpeechSegmentReady:
		e.Trace = &trace
		return e
	case ThinkingComplete:
		e.Trace = &trace
		return e
	case ThinkingStarted:
		e.Trace = &trace
		return e
	case TokenUsage:
		e.Trace = &trace
		return e
	case ToolCallFinished:
		e.Trace = &trace
		return e
	case ToolCallStarted:
		e.Trace = &trace
		return e
	case TurnInterrupted:
		e.Trace = &trace
		return e
	case TurnMetrics:
		e.Trace = &trace
		return e
	case UserInput:
		e.Trace = &trace
		return e
	case VoiceGateStripped:
		e.Trace = &trace
		return e
	case VoiceGenerated:
		e.Trace = &trace
		return e
	case WorkDelivery:
		e.Trace = &trace
		return e
	case WorkStateChanged:
		e.Trace = &trace
		return e
	case WorkerChargeState:
		e.Trace = &trace
		return e
	case WorkerLimitReached:
		e.Trace = &trace
		return e
	case WorkerUsageRecorded:
		e.Trace = &trace
		return e
	case PipelineTrace:
		e.Trace = &trace
		return e
	}
	return event
}

// SourceWorkScope returns independently owned work identity, never the active reply.
func SourceWorkScope(event Event) (runtime, session, epoch string, ok bool) {
	var scope work.WorkScope
	switch e := event.(type) {
	case JobFinished:
		scope = e.Scope
	case WorkStateChanged:
		scope = e.Snapshot.Scope
	case WorkDelivery:
		scope = e.Scope
	case WorkerUsageRecorded:
		scope = e.Scope
	case WorkerLimitReached:
		scope = e.Scope
	case WorkerChargeState:
		scope = e.Scope
	case RouteDecided:
		scope = e.Scope
	case ToolCallStarted:
		scope = e.WorkScope
	case ToolCallFinished:
		scope = e.WorkScope
	default:
		return "", "", "", false
	}
	return scope.RuntimeEpoch, scope.SessionID, scope.ConversationEpoch, scope.Valid()
}

// SourceJobID is typed producer-owned job identity, independent of span naming.
func SourceJobID(event Event) string {
	switch e := event.(type) {
	case JobQueued:
		return e.ID
	case JobStarted:
		return e.ID
	case JobFinished:
		return e.ID
	case JobFailed:
		return e.ID
	case JobAborted:
		return e.ID
	case WorkerUsageRecorded:
		return e.JobID
	case WorkerLimitReached:
		return e.JobID
	case WorkerChargeState:
		return e.JobID
	case ToolCallStarted:
		return e.JobID
	case ToolCallFinished:
		return e.JobID
	}
	return ""
}
