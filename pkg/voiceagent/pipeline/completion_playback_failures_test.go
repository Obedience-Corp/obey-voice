package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestCompletionPlaybackLeaseFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"bind_failure", "unsupported", "finish_failure", "canceled_response"} {
		t.Run(mode, func(t *testing.T) {
			p, c, scope := newCompletionLeasePipeline(t, true)
			permit, err := c.AcquireCapturePermit(scope, "test", 13)
			if err != nil {
				t.Fatal(err)
			}
			token := stt.PauseToken{Scope: scope, RequestID: "pause", Epoch: 13, Permit: permit}
			session := &playbackCaptureTestSession{expected: token}
			p.captureSession, p.captureToken, p.captureYielded = session, token, true
			failure := errors.New("capture owner failure")
			if mode == "bind_failure" {
				session.badToken = failure
			}
			if mode == "unsupported" {
				session.badToken = stt.ErrCompletionPlaybackUnsupported
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			err = p.withCompletionLease(ctx, func(context.Context, work.CompletionLease, []work.ResultView) error {
				called = true
				if mode == "finish_failure" {
					session.badToken = failure
				}
				if mode == "canceled_response" {
					cancel()
					return ctx.Err()
				}
				return nil
			})
			switch mode {
			case "bind_failure":
				if called || !errors.Is(err, failure) {
					t.Fatalf("generation=%v error=%v", called, err)
				}
			case "unsupported":
				if called || err != nil {
					t.Fatalf("unsupported source generated=%v error=%v", called, err)
				}
			case "finish_failure":
				if !called || !errors.Is(err, failure) {
					t.Fatalf("cleanup error lost: generated=%v error=%v", called, err)
				}
			case "canceled_response":
				if !called || !errors.Is(err, context.Canceled) || len(session.finishes) != 1 || session.finishCtxErr != nil {
					t.Fatalf("canceled cleanup: generated=%v finishes=%v context=%v error=%v", called, session.finishes, session.finishCtxErr, err)
				}
			}
			if permit.Valid() {
				t.Fatal("attempt exit left capture permit valid")
			}
		})
	}
}

type gatedPlaybackCapture struct {
	*playbackCaptureTestSession
	confirming chan struct{}
	release    chan struct{}
}

func (s *gatedPlaybackCapture) ConfirmCompletionPlayback(ctx context.Context, token stt.PauseToken, attempt string) error {
	close(s.confirming)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.playbackCaptureTestSession.ConfirmCompletionPlayback(ctx, token, attempt)
}

func TestCompletionPlaybackStopsAndFencesBeforeCaptureAcknowledgement(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, true)
	permit, err := c.AcquireCapturePermit(scope, "stream", 19)
	if err != nil {
		t.Fatal(err)
	}
	token := stt.PauseToken{Scope: scope, RequestID: "pause", Epoch: 19, Permit: permit}
	session := &gatedPlaybackCapture{playbackCaptureTestSession: &playbackCaptureTestSession{expected: token}, confirming: make(chan struct{}), release: make(chan struct{})}
	p.captureSession, p.captureToken, p.captureYielded = session, token, true
	capture := newFakeCapture()
	player := newFakePlayer(3 * time.Second)
	defer player.Close()
	p.Capture, p.Player, p.TTS, p.BargeInVAD = capture, player, &fakeTTS{}, &fakeVAD{}
	leaseReady := make(chan work.CompletionLease, 1)
	finished := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	go func() {
		finished <- p.withCompletionLease(ctx, func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
			leaseReady <- lease
			metrics := newTurnMetrics()
			metrics.delivery = &completionPlayback{lease: lease}
			turn := p.newTurnConductor(metrics)
			turn.to(TurnThinking)
			_, interrupted, err := p.streamResponse(ctx, nil, completionTextStream("The background result is ready."), true, metrics, turn, nil)
			if !interrupted {
				return errors.New("qualified speech did not interrupt completion stream")
			}
			return err
		})
	}()
	lease := receiveCapture(t, "completion lease", leaseReady)
	receiveCapture(t, "speaker start", player.StartedSignal())
	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{.9, .9, .9})
	}
	select {
	case <-session.confirming:
	case <-ctx.Done():
		t.Fatal("qualified interruption did not reach capture owner")
	}
	if player.StopCount() == 0 || lease.Valid() {
		t.Fatalf("capture acknowledgement blocked before stop/fence: stops=%d valid=%v", player.StopCount(), lease.Valid())
	}
	close(session.release)
	if err := receiveCapture(t, "completion finish", finished); err != nil {
		t.Fatal(err)
	}
	if len(session.retains) != 1 || !session.retains[0] || session.finishCtxErr != nil {
		t.Fatalf("qualified input cleanup=%v context=%v", session.retains, session.finishCtxErr)
	}
}
