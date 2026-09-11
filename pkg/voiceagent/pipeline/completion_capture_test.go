package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const captureTestWait = 3 * time.Second

type captureReuseSession struct {
	events chan stt.Event

	mu           sync.Mutex
	coordinator  *work.Coordinator
	scope        work.WorkScope
	token        stt.PauseToken
	paused       chan struct{}
	started      chan struct{}
	startRelease chan struct{}
	bound        chan struct{}
	pauseToken   chan stt.PauseToken
	ackRelease   chan struct{}
	resumed      chan struct{}
	finalOnce    sync.Once
	closes       atomic.Int32
}

func newCaptureReuseSession() *captureReuseSession {
	return &captureReuseSession{
		events:       make(chan stt.Event, 8),
		paused:       make(chan struct{}, 1),
		started:      make(chan struct{}, 1),
		startRelease: make(chan struct{}),
		bound:        make(chan struct{}, 1),
		pauseToken:   make(chan stt.PauseToken, 1),
		ackRelease:   make(chan struct{}),
		resumed:      make(chan struct{}, 1),
	}
}

func (s *captureReuseSession) Events() <-chan stt.Event { return s.events }
func (s *captureReuseSession) Close() error {
	s.closes.Add(1)
	return nil
}
func (s *captureReuseSession) BindCompletionCoordinator(c *work.Coordinator) {
	s.mu.Lock()
	s.coordinator = c
	s.mu.Unlock()
	s.bound <- struct{}{}
}
func (s *captureReuseSession) HasCapturedInput() bool { return false }
func (s *captureReuseSession) PauseForCompletion(ctx context.Context, scope work.WorkScope, requestID string) (stt.PauseResult, error) {
	s.mu.Lock()
	c := s.coordinator
	s.scope = scope
	s.mu.Unlock()
	if c == nil {
		return stt.PauseResult{}, errors.New("capture coordinator not bound")
	}
	permit, err := c.AcquireCapturePermit(scope, requestID, 17)
	if err != nil {
		return stt.PauseResult{}, err
	}
	token := stt.PauseToken{Scope: scope, RequestID: requestID, Epoch: 17, Permit: permit}
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
	s.paused <- struct{}{}
	s.pauseToken <- token
	select {
	case <-ctx.Done():
		return stt.PauseResult{}, ctx.Err()
	case <-s.ackRelease:
		return stt.PauseResult{Decision: stt.PauseAcknowledged, Token: token}, nil
	}
}
func (s *captureReuseSession) ResumeCapture(context.Context, stt.PauseToken) error {
	s.resumed <- struct{}{}
	s.finalOnce.Do(func() { s.events <- stt.FinalTranscript{Text: "retained final"} })
	return nil
}

type captureReuseProvider struct {
	session *captureReuseSession
	starts  atomic.Int32
}

func (p *captureReuseProvider) Start(context.Context) (stt.Session, error) {
	p.starts.Add(1)
	p.session.events <- stt.PhaseEvent{Phase: "listening"}
	p.session.started <- struct{}{}
	<-p.session.startRelease
	return p.session, nil
}
func (*captureReuseProvider) Available() bool { return true }

type resetCapture struct{ resets atomic.Int32 }

func (c *resetCapture) Subscribe(int) (int, <-chan []float32) { return 1, make(chan []float32) }
func (*resetCapture) Unsubscribe(int)                         {}
func (c *resetCapture) Reset()                                { c.resets.Add(1) }

func receiveCapture[T any](t *testing.T, name string, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(captureTestWait)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}

func TestRunTurnReusesYieldedCaptureAndRetainsFinalTranscript(t *testing.T) {
	p, coordinator, scope := newCompletionLeasePipeline(t, true)
	brain := &fakeBrain{chunks: []string{"acknowledged"}}
	p.Brain = brain
	session := newCaptureReuseSession()
	provider := &captureReuseProvider{session: session}
	p.STT = provider
	capture := &resetCapture{}
	p.Capture = capture

	var userInputs atomic.Int32
	var timeouts atomic.Int32
	var yielded atomic.Int32
	metrics := make(chan events.TurnMetrics, 4)
	events.Subscribe(p.Events, func(e events.UserInput) { userInputs.Add(1) })
	events.Subscribe(p.Events, func(e events.TurnMetrics) {
		metrics <- e
		if e.Outcome == "yielded_for_completion" {
			yielded.Add(1)
		}
		if e.Outcome == "timed_out" {
			timeouts.Add(1)
		}
	})

	turnCtx, cancelTurns := context.WithTimeout(context.Background(), captureTestWait)
	t.Cleanup(cancelTurns)
	first := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(turnCtx)
		first <- err
	}()
	receiveCapture(t, "capture start", session.started)
	close(session.startRelease)
	receiveCapture(t, "capture binding", session.bound)

	pauseDone := make(chan error, 1)
	go func() {
		acknowledged, err := p.PauseForCompletion(turnCtx)
		if err == nil && !acknowledged {
			err = errors.New("capture pause was denied")
		}
		pauseDone <- err
	}()
	token := receiveCapture(t, "pause token", session.pauseToken)
	session.mu.Lock()
	if session.token != token {
		t.Errorf("pause token channel = %+v, stored token = %+v", token, session.token)
	}
	session.mu.Unlock()
	session.events <- stt.YieldedForCompletion{Scope: scope, RequestID: token.RequestID, Epoch: token.Epoch, Token: token}
	close(session.ackRelease)
	if err := receiveCapture(t, "pause acknowledgment", pauseDone); err != nil {
		t.Fatal(err)
	}
	firstErr := receiveCapture(t, "yielded turn", first)
	if firstErr != nil {
		t.Fatalf("yielded RunTurn error = %v", firstErr)
	}
	receiveCapture(t, "yielded metrics", metrics)

	if got := provider.starts.Load(); got != 1 {
		t.Fatalf("STT starts after yield = %d, want 1", got)
	}
	if got := capture.resets.Load(); got != 1 {
		t.Fatalf("capture resets after first turn = %d, want 1", got)
	}
	p.captureMu.Lock()
	storedToken := p.captureToken
	p.captureMu.Unlock()
	if storedToken != token {
		t.Fatalf("yielded pause token = %+v, want event token %+v", storedToken, token)
	}
	if got := yielded.Load(); got != 1 {
		t.Fatalf("yielded metrics = %d, want 1", got)
	}
	if userInputs.Load() != 0 || timeouts.Load() != 0 {
		t.Fatalf("yield emitted synthetic events: userInputs=%d timeouts=%d", userInputs.Load(), timeouts.Load())
	}

	// Hold generation under the completion lease, then revoke the speech
	// attempt. The pending result remains available while the capture token is
	// invalidated; the next turn must resume this same session and consume its
	// retained final transcript once.
	leaseSeen := make(chan work.CompletionLease, 1)
	leaseCtx, cancelLease := context.WithTimeout(context.Background(), captureTestWait)
	t.Cleanup(cancelLease)
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- p.withCompletionLease(leaseCtx, func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
			leaseSeen <- lease
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	lease := receiveCapture(t, "completion lease", leaseSeen)
	if err := coordinator.RevokeCompletionLease(lease); err != nil {
		t.Fatal(err)
	}
	leaseErr := receiveCapture(t, "revoked generation", leaseDone)
	if !errors.Is(leaseErr, context.Canceled) {
		t.Fatalf("revoked generation error = %v", leaseErr)
	}

	second := make(chan struct {
		text string
		err  error
	}, 1)
	p.keepCapture.Store(true)
	go func() {
		text, err := p.RunTurn(turnCtx)
		second <- struct {
			text string
			err  error
		}{text, err}
	}()
	receiveCapture(t, "capture resume", session.resumed)
	result := receiveCapture(t, "retained-final turn", second)
	if result.err != nil {
		t.Fatalf("retained-final RunTurn error = %v", result.err)
	}
	if result.text != "retained final" {
		t.Fatalf("retained final text = %q", result.text)
	}
	if got := provider.starts.Load(); got != 1 {
		t.Fatalf("STT starts after resume = %d, want 1", got)
	}
	if got := capture.resets.Load(); got != 1 {
		t.Fatalf("capture resets after resume = %d, want 1", got)
	}
	if p.keepCapture.Load() {
		t.Fatal("resume left retained-audio marker set")
	}
	if session.closes.Load() != 1 {
		t.Fatalf("session closes = %d, want one final close", session.closes.Load())
	}
}
