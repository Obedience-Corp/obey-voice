package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type pausedSTTSession struct{}

func (pausedSTTSession) Events() <-chan stt.Event { return make(chan stt.Event) }
func (pausedSTTSession) Close() error             { return nil }

type pausedSTTProvider struct{}

func (pausedSTTProvider) Start(context.Context) (stt.Session, error) { return pausedSTTSession{}, nil }
func (pausedSTTProvider) Available() bool                            { return true }

func newCompletionLeasePipeline(t *testing.T, voice bool) (*Pipeline, *work.Coordinator, work.WorkScope) {
	t.Helper()
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	brainProvider := &lifecycleWorker{reply: "brain"}
	p := &Pipeline{Events: events.NewBus(), Brain: brainProvider}
	worker := &lifecycleWorker{reply: "worker"}
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	p.Agency = a
	if voice {
		p.STT = pausedSTTProvider{}
	}
	if err := p.EnableConversationWork("completion-test", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	c := p.WorkCoordinator()
	scope := p.WorkSnapshot().Scope
	key, err := c.Register(scope, work.Admission{Task: "completion"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(key, work.Outcome{
		Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "pending result",
	}); err != nil {
		t.Fatal(err)
	}
	return p, c, scope
}

func TestCompletionLeaseCancelsGenerationAndPreservesPending(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- p.withCompletionLease(context.Background(), func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
			close(entered)
			if err := c.RevokeCompletionLease(lease); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-entered
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("lease callback error = %v, want cancellation", err)
	}
	got := c.Pending(scope)
	if len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("canceled generation changed pending result: %+v", got)
	}
}

func TestCompletionLeaseHasOnePipelineCallbackOwner(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	var callbacks atomic.Int32
	callback := func(ctx context.Context, _ work.CompletionLease, _ []work.ResultView) error {
		if callbacks.Add(1) == 1 {
			close(entered)
			<-release
		}
		return ctx.Err()
	}
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- p.withCompletionLease(context.Background(), callback) }()
	<-entered
	go func() { second <- p.withCompletionLease(context.Background(), callback) }()
	if err := <-second; !errors.Is(err, work.ErrConflict) {
		t.Fatalf("second completion attempt error = %v, want conflict", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first completion attempt error = %v", err)
	}
	if got := callbacks.Load(); got != 1 {
		t.Fatalf("completion callback count = %d, want 1", got)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("concurrent attempts changed pending result: %+v", got)
	}
}

func TestReplacementRevokesCompletionAttemptAndPreservesResult(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- p.withCompletionLease(context.Background(), func(ctx context.Context, _ work.CompletionLease, _ []work.ResultView) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-entered
	if err := p.ReplaceConversationBrain(&lifecycleWorker{reply: "next"}, &lifecycleWorker{reply: "next-worker"}, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("replaced attempt error = %v, want cancellation", err)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("replacement changed pending result: %+v", got)
	}
}

func TestMutedCompletionSkipsLease(t *testing.T) {
	p, _, _ := newCompletionLeasePipeline(t, false)
	p.SetOutputMuted(true)
	var callbacks atomic.Int32
	if err := p.withCompletionLease(context.Background(), func(context.Context, work.CompletionLease, []work.ResultView) error {
		callbacks.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Fatal("muted completion invoked generation callback")
	}
}

func TestTextOnlyCompletionLeaseCarriesAndFencesPlaybackAttempt(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	var wrote atomic.Bool
	if err := p.withCompletionLease(context.Background(), func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
		attempt := audio.PlaybackAttemptFromContext(ctx)
		if attempt.ID != lease.AttemptID || attempt.CaptureEpoch != lease.CaptureEpoch || attempt.Fence == nil || !attempt.Valid() {
			t.Errorf("playback attempt = %+v, lease = %+v", attempt, lease)
		}
		if err := c.RevokeBinding(scope); err != nil {
			t.Errorf("revoke binding: %v", err)
		}
		if attempt.Valid() || attempt.Write(func() { wrote.Store(true) }) {
			t.Error("revoked playback attempt remained writable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if wrote.Load() {
		t.Fatal("revoked playback callback ran")
	}
}

func TestVoiceCompletionUsesAcknowledgedPausedCapturePermit(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, true)
	permit, err := c.AcquireCapturePermit(scope, "paused-stt", 7)
	if err != nil {
		t.Fatal(err)
	}
	p.captureSession = pausedSTTSession{}
	p.captureToken = stt.PauseToken{Scope: scope, RequestID: "pause-1", Epoch: 7, Permit: permit}
	p.captureYielded = true
	var called atomic.Bool
	if err := p.withCompletionLease(context.Background(), func(_ context.Context, lease work.CompletionLease, _ []work.ResultView) error {
		called.Store(lease.Valid())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("acknowledged paused capture did not grant a completion lease")
	}
}
