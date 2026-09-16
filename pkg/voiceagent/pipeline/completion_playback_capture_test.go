package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type playbackCaptureTestSession struct {
	mu            sync.Mutex
	bound         []audio.PlaybackAttempt
	confirms      []string
	finishes      []string
	retains       []bool
	expected      stt.PauseToken
	badToken      error
	confirmErr    error
	confirmCtxErr error
	finishErr     error
	finishCtxErr  error
}

func (s *playbackCaptureTestSession) Events() <-chan stt.Event                  { return make(chan stt.Event) }
func (*playbackCaptureTestSession) Close() error                                { return nil }
func (*playbackCaptureTestSession) BindCompletionCoordinator(*work.Coordinator) {}
func (*playbackCaptureTestSession) PauseForCompletion(context.Context, work.WorkScope, string) (stt.PauseResult, error) {
	return stt.PauseResult{}, errors.New("not used")
}
func (*playbackCaptureTestSession) ResumeCapture(context.Context, stt.PauseToken) error { return nil }
func (*playbackCaptureTestSession) HasCapturedInput() bool                              { return false }

func (s *playbackCaptureTestSession) checkToken(token stt.PauseToken) error {
	if token != s.expected {
		return errors.New("unexpected token")
	}
	if s.badToken != nil {
		return s.badToken
	}
	return nil
}

func (s *playbackCaptureTestSession) BindCompletionPlayback(_ context.Context, token stt.PauseToken, attempt audio.PlaybackAttempt, _ time.Duration) error {
	if err := s.checkToken(token); err != nil {
		return err
	}
	s.mu.Lock()
	s.bound = append(s.bound, attempt)
	s.mu.Unlock()
	return nil
}
func (s *playbackCaptureTestSession) ConfirmCompletionPlayback(ctx context.Context, token stt.PauseToken, attemptID string) error {
	if err := s.checkToken(token); err != nil {
		return err
	}
	s.mu.Lock()
	s.confirms = append(s.confirms, attemptID)
	s.confirmCtxErr = ctx.Err()
	s.mu.Unlock()
	return s.confirmErr
}
func (s *playbackCaptureTestSession) FinishCompletionPlayback(ctx context.Context, token stt.PauseToken, attemptID string, retain bool) error {
	if err := s.checkToken(token); err != nil {
		return err
	}
	s.mu.Lock()
	s.finishes = append(s.finishes, attemptID)
	s.retains = append(s.retains, retain)
	s.finishCtxErr = ctx.Err()
	s.mu.Unlock()
	return s.finishErr
}

func TestCompletionPlaybackCaptureBindsAndFinishesSameAttempt(t *testing.T) {
	session := &playbackCaptureTestSession{}
	token := stt.PauseToken{RequestID: "pause-1", Epoch: 4}
	session.expected = token
	p := &Pipeline{captureSession: session, captureToken: token}
	attempt := audio.PlaybackAttempt{ID: "attempt-1", CaptureEpoch: token.Epoch, Render: &audio.FirstRenderBoundary{}}
	ctx, capture, err := p.bindCompletionPlayback(context.Background(), attempt)
	if err != nil {
		t.Fatalf("bindCompletionPlayback: %v", err)
	}
	if capture == nil || completionCaptureFromContext(ctx) != capture {
		t.Fatal("bound capture was not carried in context")
	}
	if len(session.bound) != 1 || session.bound[0].ID != attempt.ID || session.bound[0].Render != attempt.Render {
		t.Fatalf("bound attempt = %+v, want original attempt", session.bound)
	}
	if err := capture.confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := capture.finish(ctx); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if len(session.confirms) != 1 || len(session.finishes) != 1 || len(session.retains) != 1 || !session.retains[0] || session.confirms[0] != attempt.ID || session.finishes[0] != attempt.ID || session.finishCtxErr != nil {
		t.Fatalf("lifecycle calls = confirms %v finishes %v", session.confirms, session.finishes)
	}
}

func TestCompletionPlaybackCaptureFinishSurvivesCancellationAndKeepsStaleOwner(t *testing.T) {
	oldOwner := &playbackCaptureTestSession{}
	newOwner := &playbackCaptureTestSession{}
	token := stt.PauseToken{RequestID: "pause-1", Epoch: 8}
	oldOwner.expected = token
	p := &Pipeline{captureSession: oldOwner, captureToken: token}
	ctx, capture, err := p.bindCompletionPlayback(context.Background(), audio.PlaybackAttempt{ID: "old-attempt", CaptureEpoch: 8})
	if err != nil {
		t.Fatal(err)
	}
	p.captureSession, p.captureToken = newOwner, stt.PauseToken{RequestID: "new", Epoch: 9}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := capture.finish(canceled); err != nil {
		t.Fatalf("finish after cancellation: %v", err)
	}
	if len(oldOwner.finishes) != 1 || oldOwner.finishes[0] != "old-attempt" {
		t.Fatalf("old owner finish calls = %v", oldOwner.finishes)
	}
	if len(newOwner.finishes) != 0 {
		t.Fatalf("replacement owner received stale cleanup: %v", newOwner.finishes)
	}
}

func TestCompletionPlaybackCaptureRejectsStaleOwnerToken(t *testing.T) {
	want := errors.New("stale token")
	session := &playbackCaptureTestSession{expected: stt.PauseToken{RequestID: "pause", Epoch: 1}}
	p := &Pipeline{captureSession: session, captureToken: stt.PauseToken{RequestID: "pause", Epoch: 1}}
	_, capture, err := p.bindCompletionPlayback(context.Background(), audio.PlaybackAttempt{ID: "attempt", CaptureEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	session.badToken = want
	if err := capture.confirm(context.Background()); !errors.Is(err, want) {
		t.Fatalf("confirm error = %v, want %v", err, want)
	}
	if err := capture.finish(context.Background()); !errors.Is(err, want) {
		t.Fatalf("finish error = %v, want %v", err, want)
	}
}

func TestCompletionPlaybackCaptureRetainsAfterConfirmCancellation(t *testing.T) {
	want := errors.New("confirm canceled")
	token := stt.PauseToken{RequestID: "pause", Epoch: 3}
	session := &playbackCaptureTestSession{expected: token, confirmErr: want}
	p := &Pipeline{captureSession: session, captureToken: token}
	_, capture, err := p.bindCompletionPlayback(context.Background(), audio.PlaybackAttempt{ID: "attempt", CaptureEpoch: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := capture.confirm(ctx); !errors.Is(err, want) {
		t.Fatalf("confirm error = %v, want %v", err, want)
	}
	if session.confirmCtxErr == nil {
		t.Fatal("confirm fake did not observe canceled context")
	}
	if err := capture.finish(ctx); err != nil {
		t.Fatalf("finish after canceled confirm: %v", err)
	}
	if len(session.retains) != 1 || !session.retains[0] {
		t.Fatalf("retain flags = %v, want [true]", session.retains)
	}
	if session.finishCtxErr != nil {
		t.Fatalf("finish context = %v, want uncanceled", session.finishCtxErr)
	}
}

func TestWithCompletionLeaseBindsBeforeCallbackAndFinishesOnError(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, true)
	permit, err := c.AcquireCapturePermit(scope, "pause", 11)
	if err != nil {
		t.Fatal(err)
	}
	token := stt.PauseToken{Scope: scope, RequestID: "pause", Epoch: 11, Permit: permit}
	session := &playbackCaptureTestSession{expected: token}
	p.captureMu.Lock()
	p.captureSession = session
	p.captureToken = token
	p.captureYielded = true
	p.captureGeneration = 1
	p.captureMu.Unlock()
	wantErr := errors.New("callback failed")
	err = p.withCompletionLease(context.Background(), func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
		attempt := audio.PlaybackAttemptFromContext(ctx)
		if len(session.bound) != 1 || attempt.ID != lease.AttemptID || attempt.CaptureEpoch != lease.CaptureEpoch || attempt.Render == nil {
			t.Fatalf("callback attempt=%+v bound=%d lease=%+v", attempt, len(session.bound), lease)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("lease error = %v, want %v", err, wantErr)
	}
	if len(session.finishes) != 1 || session.finishes[0] != session.bound[0].ID || session.finishCtxErr != nil {
		t.Fatalf("cleanup finishes=%v ctxerr=%v", session.finishes, session.finishCtxErr)
	}
	if len(session.retains) != 1 || session.retains[0] {
		t.Fatalf("retain flags=%v, want false", session.retains)
	}
}
