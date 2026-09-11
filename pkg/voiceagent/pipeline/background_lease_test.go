package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type canonicalLeaseBrain struct {
	fakeBrain
	block        bool
	entered      chan struct{}
	canceled     chan struct{}
	enteredOnce  sync.Once
	cancelOnce   sync.Once
	mu           sync.Mutex
	options      brain.StreamOptions
	attempt      audio.PlaybackAttempt
	acknowledged atomic.Int32
}

func (b *canonicalLeaseBrain) ThinkStream(ctx context.Context, _ string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.options = opts
	b.attempt = audio.PlaybackAttemptFromContext(ctx)
	b.mu.Unlock()
	b.enteredOnce.Do(func() { close(b.entered) })
	if b.block {
		<-ctx.Done()
		b.cancelOnce.Do(func() { close(b.canceled) })
		return nil, ctx.Err()
	}
	return scriptedStream(ctx, []string{"background result"}, nil), nil
}

func (b *canonicalLeaseBrain) AckBackgroundDelivery() { b.acknowledged.Add(1) }

func (b *canonicalLeaseBrain) observed() (brain.StreamOptions, audio.PlaybackAttempt) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.options, b.attempt
}

func TestRunBackgroundTurnUsesCanonicalLeaseContextAndRetainsPending(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p.Player = player
	p.TTS = &fakeTTS{delay: time.Millisecond}
	b := &canonicalLeaseBrain{entered: make(chan struct{}), canceled: make(chan struct{})}
	p.Brain = b
	var users atomic.Int32
	events.Subscribe(p.Events, func(events.UserInput) { users.Add(1) })

	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.entered:
	default:
		t.Fatal("canonical brain was not invoked")
	}
	opts, attempt := b.observed()
	if !opts.Background || opts.ToolsEnabled {
		t.Fatalf("background options = %+v", opts)
	}
	if attempt.ID == "" || attempt.CaptureEpoch == 0 || attempt.Fence == nil {
		t.Fatalf("brain did not receive lease playback identity: %+v", attempt)
	}
	if users.Load() != 0 {
		t.Fatalf("background turn emitted %d user inputs", users.Load())
	}
	if b.acknowledged.Load() != 0 {
		t.Fatal("canonical path used legacy AckBackgroundDelivery")
	}
	got := c.Pending(scope)
	if len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("successful canonical turn consumed pending result: %+v", got)
	}
}

func TestRunBackgroundTurnCancelsCanonicalGenerationOnBindingRevoke(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	b := &canonicalLeaseBrain{block: true, entered: make(chan struct{}), canceled: make(chan struct{})}
	p.Brain = b
	finished := make(chan error, 1)
	go func() { finished <- p.RunBackgroundTurn(context.Background()) }()
	<-b.entered
	if err := c.RevokeBinding(scope); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.canceled:
	case <-time.After(time.Second):
		t.Fatal("binding revoke did not cancel canonical generation")
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("background cancellation error = %v", err)
	}
	got := c.Pending(scope)
	if len(got) != 1 || got[0].State != work.Pending {
		t.Fatalf("canceled canonical turn changed pending result: %+v", got)
	}
}
