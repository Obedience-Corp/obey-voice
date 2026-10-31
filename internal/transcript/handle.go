package transcript

import (
	"reflect"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Handle records one bus event using the same schema as Attach.
// Unknown types are ignored (AudioLevel, TranscriptPartial, …).
func (w *Writer) Handle(e events.Event) {
	if w == nil || e == nil {
		return
	}
	if !recordedEventTypes[events.EventType(e)] {
		return
	}
	if w.capture != nil {
		w.copyMu.Lock()
		defer w.copyMu.Unlock()
		w.mu.Lock()
		closed := w.closed || w.limited.Load()
		w.mu.Unlock()
		if closed {
			return
		}
		if captureScopeConflict(w.capture.Trace, e) {
			w.scopeExcluded.Add(1)
			return
		}
		if size, ok := boundedSize(reflect.ValueOf(e), 0); !ok {
			w.skipped.Add(1)
			w.enqueue(record{Type: "record_omitted", Trace: cloneDiagnosticTrace(events.TraceOf(e)), Message: "payload exceeds bounded copy budget", SizeEstimateBytes: size, OmittedType: events.EventType(e), SizeEstimateKind: sizeEstimateKind(size)})
			return
		}
		e = copyBounded(reflect.ValueOf(e)).Interface().(events.Event)
	}
	r, ok := encodeEvent(e)
	if !ok {
		return
	}
	r.Trace = events.TraceOf(e)
	if w.capture != nil && r.Trace == nil {
		if runtime, session, epoch, ok := events.SourceWorkScope(e); ok {
			r.Trace = &events.TraceContext{RuntimeID: runtime, SessionID: session, ConversationEpoch: epoch}
		}
	}
	w.enqueue(r)
}

// AttachAll is a detachable Attach for /record. Typed subscriptions only —
// AudioLevel / TranscriptPartial never enter Handle (PR #207 review).
func (w *Writer) AttachAll(bus *events.Bus) func() {
	if w == nil || bus == nil {
		return func() {}
	}
	unsub := []func(){
		events.SubscribeDetachable(bus, func(e events.UserInput) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.InputTranscriptUpdated) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ResponseDelta) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ResponseReady) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ToolCallStarted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ToolCallFinished) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.WorkStateChanged) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.WorkerUsageRecorded) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.WorkerLimitReached) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.WorkerChargeState) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.DecisionEvaluated) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.RouteDecided) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.WorkDelivery) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.JobQueued) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.JobStarted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.JobFinished) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.JobFailed) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.JobAborted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.Error) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.Info) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.SpeechDiagnostic) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.STTPhase) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ThinkingStarted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.SpeakingStarted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.SpeakingComplete) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.SpeakingInterrupted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.TurnInterrupted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.TurnMetrics) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.VoiceGateStripped) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.TokenUsage) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.SessionWarning) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ConversationCompacted) { w.Handle(e) }),
		events.SubscribeDetachable(bus, func(e events.ConversationCleared) { w.Handle(e) }),
	}
	return func() {
		for i := len(unsub) - 1; i >= 0; i-- {
			unsub[i]()
		}
	}
}

// recordedEventTypes is the durable/harness set Attach subscribes to.
// AttachAll uses the same names so AudioLevel ticks skip encodeEvent.
var recordedEventTypes = map[string]bool{
	"pipeline_trace":           true,
	"input_transcript_updated": true,
	"user_input":               true,
	"response_delta":           true,
	"response_ready":           true,
	"tool_call_started":        true,
	"tool_call_finished":       true,
	"work_state_changed":       true,
	"worker.usage":             true,
	"worker.limit_reached":     true,
	"worker.charge":            true,
	"route.decided":            true,
	"decision.evaluated":       true,
	"work_delivery":            true,
	"job_queued":               true,
	"job_started":              true,
	"job_finished":             true,
	"job_failed":               true,
	"job_aborted":              true,
	"error":                    true,
	"info":                     true,
	"speech_diagnostic":        true,
	"stt_phase":                true,
	"thinking_started":         true,
	"speaking_started":         true,
	"speaking_complete":        true,
	"speaking_interrupted":     true,
	"turn_interrupted":         true,
	"turn_metrics":             true,
	"voice_gate_stripped":      true,
	"token_usage":              true,
	"session_warning":          true,
	"conversation_compacted":   true,
	"conversation_cleared":     true,
}

func encodeEvent(e events.Event) (record, bool) {
	switch x := e.(type) {
	case events.PipelineTrace:
		return record{Type: "trace_span", Span: &x.Span}, true
	case events.UserInput:
		return record{Type: "user", Text: x.Text, Utterance: x.Utterance.Clone()}, true
	case events.InputTranscriptUpdated:
		return record{Type: "input_transcript", InputTranscript: &x}, true
	case events.ResponseDelta:
		return record{Type: "agent_delta", Text: x.Text}, true
	case events.ResponseReady:
		return record{Type: "agent", Text: x.Response, Degraded: x.Degraded, Interrupted: x.Interrupted}, true
	case events.ToolCallStarted:
		var scope *work.WorkScope
		if x.WorkScope.Valid() {
			s := x.WorkScope
			scope = &s
		}
		return record{Type: "tool_call", Name: x.Name, ID: x.ID, Kind: x.Kind, JobID: x.JobID, Summary: x.Summary, WorkScope: scope}, true
	case events.ToolCallFinished:
		status := string(x.OutcomeStatus())
		var exitCode, httpStatus *int
		var signal, reason, inputSummary, outputRef string
		var outputBytes int64
		var truncated bool
		elapsed := x.ResultElapsed()
		if o := x.Outcome; o != nil {
			exitCode, httpStatus = o.ExitCode, o.HTTPStatus
			signal, reason, inputSummary, outputRef = o.Signal, o.Reason, o.InputSummary, o.OutputRef
			outputBytes, truncated = o.OutputBytes, o.Truncated
		}
		var scope *work.WorkScope
		if x.WorkScope.Valid() {
			s := x.WorkScope
			scope = &s
		}
		return record{
			Type: "tool_result", Name: x.Name, ID: x.ID, Kind: x.Kind, JobID: x.JobID,
			WorkScope: scope,
			Preview:   x.ResultPreview(), Message: x.ResultReason(),
			IsError:  status == string(events.ToolOutcomeFailed) || status == string(events.ToolOutcomeTimedOut) || status == string(events.ToolOutcomeCanceled),
			ElapsedS: elapsed.Seconds(),
			Status:   status, ExitCode: exitCode, HTTPStatus: httpStatus, Signal: signal, Reason: reason,
			InputSummary: inputSummary, OutputRef: outputRef, OutputBytes: outputBytes, Truncated: truncated,
		}, true
	case events.WorkStateChanged:
		return record{Type: "work_state_changed", Work: &x.Snapshot}, true
	case events.WorkerUsageRecorded:
		return record{Type: "worker.usage", WorkerUsage: &x}, true
	case events.WorkerLimitReached:
		return record{Type: "worker.limit_reached", WorkerLimit: &x}, true
	case events.WorkerChargeState:
		return record{Type: "worker.charge", WorkerCharge: &x}, true
	case events.DecisionEvaluated:
		return record{Type: "decision.evaluated", Decision: &x}, true
	case events.RouteDecided:
		return record{Type: "route.decided", WorkScope: &x.Scope, Kind: x.Kind, Tier: x.Tier, ReasonCode: x.ReasonCode, State: x.State, JobID: x.JobID, Text: x.Task, Message: x.Message}, true
	case events.WorkDelivery:
		return record{Type: "work_delivery", Delivery: &x}, true
	case events.JobQueued:
		return record{Type: "job", JobID: x.ID, Name: x.ID, Text: x.Task, State: "queued"}, true
	case events.JobStarted:
		return record{Type: "job", JobID: x.ID, Name: x.ID, Text: x.Task, State: "started"}, true
	case events.JobFinished:
		return record{Type: "job", JobID: x.ID, Name: x.ID, Text: x.Task, State: "finished", ElapsedS: x.Elapsed.Seconds()}, true
	case events.JobFailed:
		return record{Type: "job", JobID: x.ID, Name: x.ID, Text: x.Task, State: "failed", Message: x.Err, IsError: true, ElapsedS: x.Elapsed.Seconds()}, true
	case events.JobAborted:
		return record{Type: "job", JobID: x.ID, Name: x.ID, Text: x.Task, State: "aborted", Reason: x.Reason, ElapsedS: x.Elapsed.Seconds()}, true
	case events.Error:
		return record{Type: "error", Stage: x.Stage, Message: x.Message}, true
	case events.Info:
		return record{Type: "info", Message: x.Message}, true
	case events.SpeechDiagnostic:
		return record{
			Type: "speech_diagnostic", Message: x.Message, Symptom: x.Symptom,
			Cause: x.Cause, Confidence: x.Confidence, Provider: x.Provider,
			Scope: x.Scope, ActionCode: x.ActionCode, DedupeKey: x.DedupeKey,
			EngineRTF: x.EngineRTF, GapS: x.Gap.Seconds(), SampleCount: x.SampleCount,
			AudioSeconds: x.AudioSeconds, Exclusions: append([]string(nil), x.Exclusions...),
		}, true
	case events.STTPhase:
		return record{Type: "stt", Phase: x.Phase}, true
	case events.ThinkingStarted:
		return record{Type: "thinking"}, true
	case events.SpeakingStarted:
		return record{Type: "speaking", State: "started", Text: previewText(x.Text, summaryPreviewMax)}, true
	case events.SpeakingComplete:
		return record{Type: "speaking", State: "complete", Interrupted: x.Interrupted, SpokeS: x.Elapsed.Seconds()}, true
	case events.SpeakingInterrupted:
		return record{Type: "speaking", State: "interrupted", Reason: x.Reason}, true
	case events.TurnInterrupted:
		return record{Type: "turn_interrupted", Reason: x.Reason}, true
	case events.TurnMetrics:
		return record{
			Type:   "metrics",
			Timing: x.Timing, AudibleSink: x.AudibleSink, Envelope: x.Envelope,
			Outcome:            x.Outcome,
			Degraded:           x.Degraded,
			Interrupted:        x.Interrupted,
			STTFinalS:          x.STTFinalElapsed.Seconds(),
			ModelFirstS:        x.FirstModelChunkElapsed.Seconds(),
			ModelS:             x.ModelCompleteElapsed.Seconds(),
			VoiceS:             x.FirstAudioReadyElapsed.Seconds(),
			PlaybackStartS:     x.PlaybackStartElapsed.Seconds(),
			SpokeS:             x.PlaybackCompleteElapsed.Seconds(),
			BargeInS:           x.BargeInElapsed.Seconds(),
			LeakLines:          x.ToolLeakLines,
			TTSProvider:        x.TTSProvider,
			TTSTier:            x.TTSTier,
			TTSPlace:           x.TTSPlace,
			TTSStreaming:       x.TTSWorkerStreaming,
			TTSWarm:            x.TTSWarm,
			TTSFallbacks:       x.TTSFallbacks,
			FirstAudioGraceS:   x.FirstAudioGrace.Seconds(),
			StallWatchdogFired: x.StallWatchdogFired,
			DeadAirS:           x.DeadAirElapsed.Seconds(),
			Underruns:          x.Underruns,
			Segments:           segmentRecords(x.Segments),
		}, true
	case events.VoiceGateStripped:
		return record{Type: "voice_gate", LeakLines: x.Lines}, true
	case events.TokenUsage:
		return record{Type: "tokens", Prefill: x.Prefill, Gen: x.Gen}, true
	case events.SessionWarning:
		return record{Type: "session_warning", Prefill: x.PromptTokens, Threshold: x.Threshold}, true
	case events.ConversationCompacted:
		return record{Type: "compacted", TurnsBefore: x.TurnsBefore, Summary: previewText(x.Summary, summaryPreviewMax)}, true
	case events.ConversationCleared:
		return record{Type: "cleared"}, true
	default:
		return record{}, false
	}
}
