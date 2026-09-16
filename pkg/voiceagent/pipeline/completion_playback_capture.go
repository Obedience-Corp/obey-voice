package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// completionPlaybackCapture carries the original owner and token through one
// serialized response. A replacement session can never receive its cleanup.
type completionPlaybackCapture struct {
	owner     stt.CompletionPlaybackCapture
	token     stt.PauseToken
	attemptID string
	retain    bool
}

type completionPlaybackCaptureKey struct{}

func completionCaptureFromContext(ctx context.Context) *completionPlaybackCapture {
	capture, _ := ctx.Value(completionPlaybackCaptureKey{}).(*completionPlaybackCapture)
	return capture
}

func (p *Pipeline) bindCompletionPlayback(ctx context.Context, attempt audio.PlaybackAttempt) (context.Context, *completionPlaybackCapture, error) {
	p.captureMu.Lock()
	session, token := p.captureSession, p.captureToken
	p.captureMu.Unlock()
	owner, ok := session.(stt.CompletionPlaybackCapture)
	if !ok || p.externalCompletionCapture != nil {
		// Existing remote/legacy adapters retain their existing capture policy.
		return ctx, nil, nil
	}
	bindCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := owner.BindCompletionPlayback(bindCtx, token, attempt, p.capturePreRoll()); err != nil {
		return ctx, nil, fmt.Errorf("bind completion playback capture: %w", err)
	}
	capture := &completionPlaybackCapture{owner: owner, token: token, attemptID: attempt.ID}
	return context.WithValue(ctx, completionPlaybackCaptureKey{}, capture), capture, nil
}

func (c *completionPlaybackCapture) confirm(ctx context.Context) error {
	if c == nil {
		return nil
	}
	// Consuming a qualified request establishes retention intent even if the
	// response is canceled before the owner's acknowledgement. Its uncanceled
	// finish command must not trim this accepted input to ordinary pre-roll.
	c.retain = true
	confirmCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := c.owner.ConfirmCompletionPlayback(confirmCtx, c.token, c.attemptID); err != nil {
		return fmt.Errorf("confirm completion playback input: %w", err)
	}
	return nil
}

func (c *completionPlaybackCapture) finish(ctx context.Context) error {
	// Cancellation ends the response, not the retained capture owner's cleanup.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	if err := c.owner.FinishCompletionPlayback(finishCtx, c.token, c.attemptID, c.retain); err != nil {
		return fmt.Errorf("finish completion playback capture: %w", err)
	}
	return nil
}
