package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

var errYieldedForCompletion = errors.New("capture yielded for completion")

// PauseForCompletion requests the active STT owner's frame-boundary decision.
// A false result means the host must keep waiting for user input. It never
// cancels a listening turn or derives silence from advisory meter events.
func (p *Pipeline) PauseForCompletion(ctx context.Context) (bool, error) {
	c := p.WorkCoordinator()
	if c == nil || !p.BackgroundPending() || p.HasCapturedInput() {
		return false, nil
	}
	p.captureMu.Lock()
	session, generation := p.captureSession, p.captureGeneration
	p.captureMu.Unlock()
	owner, ok := session.(stt.CompletionCapture)
	if !ok {
		if session != nil {
			p.reportCaptureDeferral("the STT adapter does not support capture handoff")
		}
		return false, nil // this adapter cannot yield live capture
	}
	request := fmt.Sprintf("completion-%d", p.captureRequests.Add(1))
	pauseCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	result, err := owner.PauseForCompletion(pauseCtx, c.Scope(), request)
	if errors.Is(err, stt.ErrCompletionPauseUnsupported) {
		p.reportCaptureDeferral("the STT adapter does not support capture handoff")
		return false, nil
	}
	if err != nil || result.Decision != stt.PauseAcknowledged {
		return false, err
	}
	p.captureMu.Lock()
	if p.captureSession != nil && p.captureGeneration == generation && p.WorkCoordinator() == c {
		if p.captureToken.Epoch <= result.Token.Epoch {
			p.captureToken = result.Token
		}
		p.captureDeferral = ""
		p.captureMu.Unlock()
		return true, nil
	}
	p.captureMu.Unlock()
	return false, work.ErrStaleScope
}

// closeCompletionCapture is called at explicit conversation shutdown/clear.
// Normal completion resumes the same session, preserving its detector state.
func (p *Pipeline) closeCompletionCapture() {
	if c := p.WorkCoordinator(); c != nil {
		_ = c.RevokeBinding(c.Scope())
	}
	p.captureMu.Lock()
	session := p.captureSession
	p.captureGeneration++
	p.captureSession = nil
	p.captureToken = stt.PauseToken{}
	p.captureYielded = false
	p.captureDeferral = ""
	p.captureMu.Unlock()
	p.keepCapture.Store(false)
	p.clearPreparedCapture()
	if p.Capture != nil {
		p.Capture.Reset()
	}
	if session != nil {
		_ = session.Close()
	}
}

func (p *Pipeline) startCaptureSession(ctx context.Context) (stt.Session, uint64, error) {
	p.captureMu.Lock()
	session, token, generation := p.captureSession, p.captureToken, p.captureGeneration
	resuming := session != nil && p.captureYielded
	if resuming {
		p.captureYielded = false
	}
	p.captureMu.Unlock()
	if resuming {
		if owner, ok := session.(stt.CompletionCapture); ok {
			if err := owner.ResumeCapture(ctx, token); err != nil {
				p.captureMu.Lock()
				if p.captureSession != nil && p.captureGeneration == generation {
					p.captureYielded = true
				}
				p.captureMu.Unlock()
				return nil, 0, fmt.Errorf("resume capture: %w", err)
			}
		}
		// Resume transferred the retained utterance from the yielded owner;
		// consume the one-turn keep flag so a later fresh session does not
		// replay the same retained audio.
		p.keepCapture.Store(false)
		return session, generation, nil
	}
	if err := ctx.Err(); err != nil {
		p.clearPreparedCapture()
		if p.Capture != nil {
			p.Capture.Reset()
		}
		return nil, 0, err
	}
	preserve := p.keepCapture.Swap(false)
	prepared := p.preparedCapture.Swap(false)
	if p.Capture != nil && !preserve && !prepared {
		p.Capture.Reset()
	}
	if preserve {
		p.applyStartupAdmission(ctx)
	}
	// A prepared scope was already trimmed at the response boundary. Keep
	// everything captured since then, including speech during classification.

	startCtx := ctx
	coordinator := p.WorkCoordinator()
	if coordinator != nil {
		// A yielded turn ends before its capture session. Explicit Close below
		// still cancels and joins on all ordinary return and shutdown paths.
		startCtx = context.WithoutCancel(ctx)
	}
	session, err := p.STT.Start(startCtx)
	if err != nil {
		return nil, 0, err
	}
	if binder, ok := session.(interface{ BindCompletionCoordinator(*work.Coordinator) }); ok {
		binder.BindCompletionCoordinator(coordinator)
	}
	p.captureMu.Lock()
	p.captureSession = session
	p.captureGeneration++
	generation = p.captureGeneration
	p.captureMu.Unlock()
	return session, generation, nil
}

func (p *Pipeline) finishCaptureSession(session stt.Session, generation uint64, yielded bool) {
	p.captureMu.Lock()
	if p.captureSession != nil && p.captureGeneration == generation {
		p.captureYielded = yielded
		if !yielded {
			p.captureSession = nil
			p.captureToken = stt.PauseToken{}
		}
	}
	p.captureMu.Unlock()
	if !yielded {
		_ = session.Close()
	}
}

func (p *Pipeline) reportCaptureDeferral(reason string) {
	p.captureMu.Lock()
	changed := p.captureDeferral != reason
	p.captureDeferral = reason
	p.captureMu.Unlock()
	if changed {
		p.emit(events.Info{Message: "Background update deferred: " + reason + "."})
	}
}
