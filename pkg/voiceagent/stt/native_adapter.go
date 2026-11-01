package stt

import (
	"context"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// FinalizedInput carries audio through existing capture ownership without
// inventing a FinalTranscript or requiring a Standard recognizer.
type FinalizedInput struct{ Input *utterance.Finalized }

func (FinalizedInput) sttEvent() string { return "finalized_input" }

// AdaptNativeSession bridges notifications, not capture. Optional completion
// methods delegate to the same underlying native owner and VAD loop.
func AdaptNativeSession(ctx context.Context, owner utterance.Session, release func(utterance.AudioRef)) Session {
	bridgeCtx, cancel := context.WithCancel(ctx)
	session := &nativeAdapter{owner: owner, cancel: cancel, events: make(chan Event, 8), done: make(chan struct{}), release: release}
	go session.bridge(bridgeCtx)
	return session
}

type nativeAdapter struct {
	owner   utterance.Session
	cancel  context.CancelFunc
	events  chan Event
	done    chan struct{}
	release func(utterance.AudioRef)
}

func (s *nativeAdapter) Events() <-chan Event { return s.events }
func (s *nativeAdapter) releaseInput(input *utterance.Finalized) {
	if s.release != nil && input != nil && input.Audio != nil {
		s.release(*input.Audio)
	}
}
func (s *nativeAdapter) Close() error {
	s.cancel()
	err := s.owner.Close()
	<-s.done
	for event := range s.events {
		if final, ok := event.(FinalizedInput); ok {
			s.releaseInput(final.Input)
		}
	}
	return err
}
func (s *nativeAdapter) bridge(ctx context.Context) {
	defer close(s.done)
	defer close(s.events)
	inputEvents := s.owner.Events()
	var completions <-chan YieldedForCompletion
	if owner, ok := s.owner.(interface {
		CompletionEvents() <-chan YieldedForCompletion
	}); ok {
		completions = owner.CompletionEvents()
	}
	send := func(event Event) bool {
		select {
		case s.events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for inputEvents != nil || completions != nil {
		select {
		case <-ctx.Done():
			return
		case completion, ok := <-completions:
			if !ok {
				completions = nil
				continue
			}
			if !send(completion) {
				return
			}
		case event, ok := <-inputEvents:
			if !ok {
				inputEvents = nil
				continue
			}
			var mapped Event
			switch event.Kind {
			case utterance.Phase:
				mapped = PhaseEvent{Phase: event.Phase, Elapsed: int64(event.Elapsed)}
			case utterance.Level:
				mapped = InputLevel{Level: event.Level}
			case utterance.Timeout:
				mapped = Timeout{}
			case utterance.Failure:
				mapped = Failure{Err: event.Err}
			case utterance.FinalizedEvent:
				mapped = FinalizedInput{Input: event.Input}
			default:
				continue
			}
			if ctx.Err() != nil || !send(mapped) {
				s.releaseInput(event.Input)
				return
			}
		}
	}
}
func (s *nativeAdapter) BindCompletionCoordinator(c *work.Coordinator) {
	if owner, ok := s.owner.(interface{ BindCompletionCoordinator(*work.Coordinator) }); ok {
		owner.BindCompletionCoordinator(c)
	}
}
func (s *nativeAdapter) PauseForCompletion(ctx context.Context, scope work.WorkScope, request string) (PauseResult, error) {
	if owner, ok := s.owner.(CompletionCapture); ok {
		return owner.PauseForCompletion(ctx, scope, request)
	}
	return PauseResult{}, ErrCompletionPauseUnsupported
}
func (s *nativeAdapter) ResumeCapture(ctx context.Context, token PauseToken) error {
	if owner, ok := s.owner.(CompletionCapture); ok {
		return owner.ResumeCapture(ctx, token)
	}
	return ErrCompletionPauseUnsupported
}
func (s *nativeAdapter) HasCapturedInput() bool {
	owner, ok := s.owner.(CompletionCapture)
	return ok && owner.HasCapturedInput()
}
func (s *nativeAdapter) BindCompletionPlayback(ctx context.Context, token PauseToken, attempt audio.PlaybackAttempt, preRoll time.Duration) error {
	if owner, ok := s.owner.(CompletionPlaybackCapture); ok {
		return owner.BindCompletionPlayback(ctx, token, attempt, preRoll)
	}
	return ErrCompletionPlaybackUnsupported
}
func (s *nativeAdapter) ConfirmCompletionPlayback(ctx context.Context, token PauseToken, attemptID string) error {
	if owner, ok := s.owner.(CompletionPlaybackCapture); ok {
		return owner.ConfirmCompletionPlayback(ctx, token, attemptID)
	}
	return ErrCompletionPlaybackUnsupported
}
func (s *nativeAdapter) FinishCompletionPlayback(ctx context.Context, token PauseToken, attemptID string, retain bool) error {
	if owner, ok := s.owner.(CompletionPlaybackCapture); ok {
		return owner.FinishCompletionPlayback(ctx, token, attemptID, retain)
	}
	return ErrCompletionPlaybackUnsupported
}
