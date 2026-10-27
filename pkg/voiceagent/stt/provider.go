package stt

import (
	"context"
	"errors"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

var ErrCompletionPauseUnsupported = errors.New("STT adapter cannot yield capture for completion")

var ErrCompletionPlaybackUnsupported = errors.New("STT adapter cannot retain capture across completion playback")

// CompletionPlaybackCapture keeps ordinary interruption qualification in the
// pipeline while the existing STT owner parks rendered-period input. Binding
// precedes generation; confirmation and finish match both token and attempt.
type CompletionPlaybackCapture interface {
	CompletionCapture
	BindCompletionPlayback(context.Context, PauseToken, audio.PlaybackAttempt, time.Duration) error
	ConfirmCompletionPlayback(context.Context, PauseToken, string) error
	FinishCompletionPlayback(context.Context, PauseToken, string, bool) error
}

// PauseDecision describes the result of asking the capture owner to yield an
// idle completion. A denial is a normal race, not an STT failure: the owner
// keeps the current utterance (or its finalization) intact.
type PauseDecision string

const (
	PauseAcknowledged  PauseDecision = "acknowledged"
	PauseUserSpeaking  PauseDecision = "user_speaking"
	PauseCapturedInput PauseDecision = "captured_input"
	PauseFinalizing    PauseDecision = "finalizing"
)

// PauseToken identifies one acknowledged completion pause. Epoch is owned by
// the capture owner and changes when speech arrives after acknowledgement;
// callers must present the token unchanged when resuming or claiming work.
type PauseToken struct {
	Scope     work.WorkScope
	RequestID string
	Epoch     uint64
	Permit    work.CapturePermit
}

// PauseResult is returned for both accepted and denied pause requests.
type PauseResult struct {
	Token    PauseToken
	Decision PauseDecision
}

// CompletionCapture is the serialized capture-owner boundary used by the
// background completion scheduler. Implementations keep the same source and
// ring alive after acknowledgement. Resume transfers retained frames to the
// next STT session; it must not reset or drain the source.
type CompletionCapture interface {
	BindCompletionCoordinator(*work.Coordinator)
	PauseForCompletion(ctx context.Context, scope work.WorkScope, requestID string) (PauseResult, error)
	ResumeCapture(ctx context.Context, token PauseToken) error
	HasCapturedInput() bool
}

// CompletionSession is implemented by providers that can keep one STT loop
// alive across a yielded completion. It is optional so finite/test sessions
// implementing Session remain source-compatible.
type CompletionSession interface {
	Session
	CompletionCapture
}

// Event is the base interface for STT session events.
type Event interface {
	sttEvent() string
}

// PhaseEvent reports a phase transition inside the STT session.
type PhaseEvent struct {
	Phase   string
	Elapsed int64 // nanoseconds spent in the previous phase
}

func (e PhaseEvent) sttEvent() string { return "phase" }

// PartialTranscript reports an incremental transcript update.
type PartialTranscript struct {
	Text string
}

func (e PartialTranscript) sttEvent() string { return "partial_transcript" }

// FinalTranscript reports the committed transcript for the turn.
type FinalTranscript struct {
	// AcousticName is independent keyword evidence from this utterance only.
	AcousticName string
	Text         string
}

func (e FinalTranscript) sttEvent() string { return "final_transcript" }

// Timeout reports that no speech was detected before the listen timeout.
type Timeout struct{}

func (e Timeout) sttEvent() string { return "timeout" }

// Failure reports an STT session error.
type Failure struct {
	Err error
}

func (e Failure) sttEvent() string { return "failure" }

// InputLevel is a throttled mic energy sample for UI meters (0..1).
// It is advisory and droppable — loops must never block on delivery.
type InputLevel struct {
	Level float64
}

func (e InputLevel) sttEvent() string { return "input_level" }

// Session produces STT events for one conversational turn.
type Session interface {
	Events() <-chan Event
	Close() error
}

// Provider is the interface all STT backends implement.
type Provider interface {
	// Start begins a session that emits STT events until the turn resolves.
	Start(ctx context.Context) (Session, error)

	// Available returns true if this provider is ready.
	Available() bool
}
