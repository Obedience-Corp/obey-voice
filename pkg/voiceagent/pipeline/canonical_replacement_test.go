package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type replacementCompletionBrain struct {
	*handoffCompletionBrain
	entered chan struct{}
	one     sync.Once
}

func (b *replacementCompletionBrain) Complete(ctx context.Context, req brain.CompletionRequest) (brain.CompletionEnvelope, error) {
	b.one.Do(func() { close(b.entered) })
	<-ctx.Done()
	return brain.CompletionEnvelope{}, ctx.Err()
}

type lateReplacementPlayer struct {
	first   chan struct{}
	release chan struct{}
}

func (p *lateReplacementPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if _, err := stream.WaitReady(context.Background()); err != nil {
		return nil, err
	}
	attempt := audio.PlaybackAttemptFromContext(ctx)
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		close(started)
		frames := 0
		for samples := range stream.Frames() {
			frames += len(samples)
			attempt.Write(func() {})
			select {
			case <-p.first:
			default:
				close(p.first)
			}
		}
		<-p.release
		done <- attempt.Result(audio.PlaybackResult{AudioSeconds: float64(frames) / 24000})
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}
func (*lateReplacementPlayer) Stop()            {}
func (*lateReplacementPlayer) IsPlaying() bool  { return false }
func (*lateReplacementPlayer) Close() error     { return nil }
func (*lateReplacementPlayer) SinkKind() string { return audio.SinkLocalSpeaker }

func TestCanonicalReplacementCancelsGenerationAndNewBindingDelivers(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	old := &replacementCompletionBrain{handoffCompletionBrain: &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "old"}}, entered: make(chan struct{})}
	next := &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "next"}}
	p.Brain = old
	done := make(chan error, 1)
	go func() { done <- p.RunBackgroundTurn(context.Background()) }()
	select {
	case <-old.entered:
	case <-time.After(time.Second):
		t.Fatal("old canonical generation did not enter")
	}
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next-worker"}, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "next"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("replaced canonical generation did not finish")
	}
	if pending := c.Pending(scope); len(pending) != 1 || pending[0].State == work.Communicated {
		t.Fatalf("stale generation consumed result: %+v", pending)
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(scope); len(got) != 0 {
		t.Fatalf("new binding did not deliver: %+v", got)
	}
}

func TestCanonicalReplacementRejectsLatePlaybackAndFreshBindingDelivers(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	old := &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "old"}}
	next := &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "next"}}
	late := &lateReplacementPlayer{first: make(chan struct{}), release: make(chan struct{})}
	p.Brain, p.Player, p.TTS = old, late, &fakeTTS{delay: time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- p.RunBackgroundTurn(context.Background()) }()
	select {
	case <-late.first:
	case <-time.After(time.Second):
		t.Fatal("old playback did not write PCM")
	}
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next-worker"}, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "next"}, nil); err != nil {
		t.Fatal(err)
	}
	close(late.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old playback did not join")
	}
	if pending := c.Pending(scope); len(pending) != 1 || pending[0].State == work.Communicated {
		t.Fatalf("late old playback consumed result: %+v", pending)
	}
	p.Player = &fencedHandoffPlayer{}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(scope); len(got) != 0 {
		t.Fatalf("fresh binding did not deliver: %+v", got)
	}
}
