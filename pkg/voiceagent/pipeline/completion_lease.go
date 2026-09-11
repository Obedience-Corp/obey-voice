package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// BackgroundCaptureReady prevents an idle host from skipping passive capture
// and dispatching unsolicited speech before the owner has examined input.
func (p *Pipeline) BackgroundCaptureReady() bool {
	if p.WorkCoordinator() == nil || p.STT == nil || p.externalCompletionCapture != nil {
		return true
	}
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	return p.captureYielded && p.captureToken.Permit.Valid()
}

// withCompletionLease binds generation and all PCM segments to the same
// revocable attempt. Worker callbacks never invoke this host-only operation.
func (p *Pipeline) withCompletionLease(ctx context.Context, run func(context.Context, work.CompletionLease, []work.ResultView) error) error {
	c := p.WorkCoordinator()
	if c == nil || p.HasCapturedInput() || p.OutputMuted() {
		return nil
	}
	scope := c.Scope()
	var selected []work.ResultView
	var refs []work.ResultRef
	for _, r := range c.Pending(scope) {
		if r.State != work.Pending && r.State != work.Ready {
			continue
		}
		selected = append(selected, r)
		refs = append(refs, work.ResultRef{Scope: scope, JobID: r.Outcome.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision})
		if len(refs) == 3 {
			break
		}
	}
	if len(refs) == 0 {
		return nil
	}
	request := fmt.Sprintf("response-%d", p.captureRequests.Add(1))
	p.captureMu.Lock()
	permit := p.captureToken.Permit
	p.captureMu.Unlock()
	if p.externalCompletionCapture != nil {
		var err error
		permit, err = p.externalCompletionCapture(ctx, c, request)
		if errors.Is(err, work.ErrConflict) {
			return nil
		}
		if err != nil {
			return err
		}
	} else if p.STT == nil {
		var err error
		permit, err = c.AcquireCapturePermit(scope, request, p.captureRequests.Load())
		if err != nil {
			return err
		}
	} else if !p.BackgroundCaptureReady() {
		return nil
	}
	lease, err := c.Claim(scope, request, request, refs, permit)
	if err != nil {
		if p.STT == nil || p.externalCompletionCapture != nil {
			_ = c.ReleaseCapturePermit(permit)
		}
		return err
	}
	defer func() { _ = c.ReleaseCompletionLease(lease) }()
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-lease.Done():
			cancel()
		case <-attemptCtx.Done():
			_ = c.RevokeCompletionLease(lease)
		case <-done:
		}
	}()
	defer func() { close(done); <-joined }()
	attemptCtx = audio.WithPlaybackAttempt(attemptCtx, audio.PlaybackAttempt{ID: lease.AttemptID, CaptureEpoch: lease.CaptureEpoch, Fence: lease})
	if !lease.Valid() || p.HasCapturedInput() {
		return context.Canceled
	}
	return run(attemptCtx, lease, selected)
}

// SetCompletionCapture installs a host's capture ownership boundary before
// starting its dispatcher. Remote push-to-talk hosts have no passive STT
// session; their mic-claim owner fences the next voice_start instead.
func (p *Pipeline) SetCompletionCapture(acquire func(context.Context, *work.Coordinator, string) (work.CapturePermit, error)) {
	p.externalCompletionCapture = acquire
}
