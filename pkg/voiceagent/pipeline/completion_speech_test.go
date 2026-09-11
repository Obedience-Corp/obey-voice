package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const speechLeaseTestWait = 3 * time.Second

func receiveSpeechLease[T any](t *testing.T, name string, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(speechLeaseTestWait)
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

type latePCMProvider struct {
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
	started  atomic.Bool
}

func (p *latePCMProvider) Available() bool                     { return true }
func (*latePCMProvider) ListVoices(string, string) []tts.Voice { return nil }

func (p *latePCMProvider) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	if p.started.CompareAndSwap(false, true) {
		close(p.entered)
	}
	<-p.release // deliberately ignores cancellation and returns late PCM
	defer close(p.finished)
	stream := audio.NewPCMStream(context.Background())
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	if err := stream.Write([]float32{0.25, 0.25}); err != nil {
		return nil, err
	}
	stream.Close()
	return stream, nil
}

type fencedSpeechEngine struct {
	started    chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	writes     atomic.Int32
	lateWrites atomic.Int32
	finished   chan struct{}
	resultMu   sync.Mutex
	result     audio.PlaybackResult
	attemptMu  sync.Mutex
	attempt    audio.PlaybackAttempt
}

func (e *fencedSpeechEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	// Deliberately ignore the canceled context while consuming the late stream;
	// the PlaybackAttempt fence, rather than this fake's control flow, must
	// reject stale writes.
	if _, err := stream.WaitReady(context.Background()); err != nil {
		return nil, err
	}
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	attempt := audio.PlaybackAttemptFromContext(ctx)
	e.attemptMu.Lock()
	e.attempt = attempt
	e.attemptMu.Unlock()
	go func() {
		defer close(e.finished)
		for range stream.Frames() {
			attempt.Write(func() { e.writes.Add(1) })
			e.startOnce.Do(func() { close(started); close(e.started) })
			<-e.release
			attempt.Write(func() { e.lateWrites.Add(1) })
		}
		result := attempt.Result(audio.PlaybackResult{Interrupted: true, Err: ctx.Err()})
		e.resultMu.Lock()
		e.result = result
		e.resultMu.Unlock()
		done <- result
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}

func (e *fencedSpeechEngine) playbackResult() audio.PlaybackResult {
	e.resultMu.Lock()
	defer e.resultMu.Unlock()
	return e.result
}
func (*fencedSpeechEngine) Stop()             {}
func (e *fencedSpeechEngine) IsPlaying() bool { return false }
func (*fencedSpeechEngine) Close() error      { return nil }

func (e *fencedSpeechEngine) observedAttempt() audio.PlaybackAttempt {
	e.attemptMu.Lock()
	defer e.attemptMu.Unlock()
	return e.attempt
}

func TestBackgroundRevocationDuringSynthesisRejectsLatePCM(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	tts := &latePCMProvider{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	p.TTS = tts
	engine := &fencedSpeechEngine{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	p.Player = engine
	p.Brain = &backgroundBrain{}
	t.Cleanup(func() {
		select {
		case <-tts.release:
		default:
			close(tts.release)
		}
		select {
		case <-engine.release:
		default:
			close(engine.release)
		}
	})
	done := make(chan error, 1)
	go func() { done <- p.RunBackgroundTurn(context.Background()) }()
	receiveSpeechLease(t, "synthesis entry", tts.entered)
	if err := c.RevokeBinding(scope); err != nil {
		t.Fatal(err)
	}
	close(tts.release)
	receiveSpeechLease(t, "late synthesis producer", tts.finished)
	close(engine.release)
	receiveSpeechLease(t, "late synthesis consumer", engine.finished)
	if err := receiveSpeechLease(t, "background completion", done); !errors.Is(err, context.Canceled) {
		t.Fatalf("background completion error = %v, want cancellation", err)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("revoked synthesis changed pending result: %+v", got)
	}
	if engine.writes.Load() != 0 || engine.lateWrites.Load() != 0 {
		t.Fatalf("revoked synthesis PCM writes = %d late=%d, want zero", engine.writes.Load(), engine.lateWrites.Load())
	}
	attempt := engine.observedAttempt()
	result := engine.playbackResult()
	if attempt.ID == "" || attempt.CaptureEpoch == 0 || result.AttemptID != attempt.ID || result.CaptureEpoch != attempt.CaptureEpoch {
		t.Fatalf("synthesis playback identity attempt=%+v result=%+v", attempt, result)
	}
	if b := p.Brain.(*backgroundBrain); b.acked != 0 {
		t.Fatal("revoked synthesis acknowledged background delivery")
	}
}

func TestBackgroundRevocationDuringPlaybackFencesLatePCM(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	engine := &fencedSpeechEngine{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	tts := &latePCMProvider{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	p.TTS = tts
	close(tts.release)
	p.Player = engine
	p.Brain = &backgroundBrain{}
	t.Cleanup(func() {
		select {
		case <-engine.release:
		default:
			close(engine.release)
		}
	})
	done := make(chan error, 1)
	go func() { done <- p.RunBackgroundTurn(context.Background()) }()
	receiveSpeechLease(t, "playback start", engine.started)
	attempt := engine.observedAttempt()
	if attempt.ID == "" || attempt.CaptureEpoch == 0 || attempt.Fence == nil {
		t.Fatalf("playback attempt missing lease identity: %+v", attempt)
	}
	if err := c.RevokeBinding(scope); err != nil {
		t.Fatal(err)
	}
	close(engine.release)
	receiveSpeechLease(t, "playback producer", tts.finished)
	receiveSpeechLease(t, "playback consumer", engine.finished)
	if err := receiveSpeechLease(t, "background completion", done); !errors.Is(err, context.Canceled) {
		t.Fatalf("background completion error = %v, want cancellation", err)
	}
	if engine.lateWrites.Load() != 0 {
		t.Fatalf("late fenced PCM writes = %d, want 0", engine.lateWrites.Load())
	}
	result := engine.playbackResult()
	if result.AttemptID != attempt.ID || result.CaptureEpoch != attempt.CaptureEpoch {
		t.Fatalf("playback result identity = %+v, attempt = %+v", result, attempt)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("revoked playback changed pending result: %+v", got)
	}
	if b, ok := p.Brain.(*backgroundBrain); ok && b.acked != 0 {
		t.Fatal("revoked playback acknowledged background delivery")
	}
}
