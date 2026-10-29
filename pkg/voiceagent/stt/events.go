package stt

import "time"

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"

// YieldedForCompletion is emitted when a live STT owner reaches a frame
// boundary after an acknowledged completion pause. It is deliberately
// distinct from Timeout and Failure: no transcript was fabricated and the
// capture owner remains active for barge-in and retained input.
type YieldedForCompletion struct {
	Scope     work.WorkScope
	RequestID string
	Epoch     uint64
	Token     PauseToken
}

func (e YieldedForCompletion) sttEvent() string { return "yielded_for_completion" }

// EventKind identifies the concrete STT event carried by a TypedEvent.
type EventKind string

const (
	KindPhase                EventKind = "phase"
	KindPartialTranscript    EventKind = "partial_transcript"
	KindFinalTranscript      EventKind = "final_transcript"
	KindTimeout              EventKind = "timeout"
	KindFailure              EventKind = "failure"
	KindInputLevel           EventKind = "input_level"
	KindYieldedForCompletion EventKind = "yielded_for_completion"
)

// TypedEvent is a serializable, uniform envelope for STT events. It carries
// error text rather than error values so it can round-trip through encoding;
// consumers that need the original error keep using the source event structs.
type TypedEvent struct {
	Kind         EventKind
	Text         string
	AcousticName string
	Phase        string
	Final        bool
	ErrText      string
	Confidence   float64
	Elapsed      time.Duration
	Level        float64 // KindInputLevel only (0..1)
	Scope        work.WorkScope
	RequestID    string
	Epoch        uint64
}

// ToTyped converts any STT event into its TypedEvent envelope.
func ToTyped(ev Event) TypedEvent {
	switch e := ev.(type) {
	case PhaseEvent:
		return TypedEvent{Kind: KindPhase, Phase: e.Phase, Elapsed: time.Duration(e.Elapsed)}
	case PartialTranscript:
		return TypedEvent{Kind: KindPartialTranscript, Text: e.Text}
	case FinalTranscript:
		return TypedEvent{Kind: KindFinalTranscript, Text: e.Text, Final: true, AcousticName: e.AcousticName}
	case Timeout:
		return TypedEvent{Kind: KindTimeout}
	case Failure:
		te := TypedEvent{Kind: KindFailure}
		if e.Err != nil {
			te.ErrText = e.Err.Error()
		}
		return te
	case InputLevel:
		return TypedEvent{Kind: KindInputLevel, Level: e.Level}
	case YieldedForCompletion:
		return TypedEvent{Kind: KindYieldedForCompletion, Scope: e.Scope, RequestID: e.RequestID, Epoch: e.Epoch}
	case nil:
		return TypedEvent{}
	default:
		return TypedEvent{Kind: EventKind(ev.sttEvent())}
	}
}
