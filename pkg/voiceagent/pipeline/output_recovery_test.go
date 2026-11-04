package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type generationTestEngine struct {
	*fakePlayer
	mu         sync.Mutex
	generation uint64
	accepted   int
}

func (e *generationTestEngine) BindPlaybackContext(ctx context.Context) context.Context {
	if _, ok := audio.PlaybackGeneration(ctx, e); ok {
		return ctx
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return audio.WithPlaybackGeneration(ctx, e, e.generation)
}
func (e *generationTestEngine) ResetOutput(context.Context) error {
	e.mu.Lock()
	e.generation++
	e.mu.Unlock()
	return nil
}
func (e *generationTestEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	e.mu.Lock()
	generation, ok := audio.PlaybackGeneration(ctx, e)
	stale := !ok || generation != e.generation
	if !stale {
		e.accepted++
	}
	e.mu.Unlock()
	if stale {
		discardPCMStream(stream)
		return nil, context.Canceled
	}
	return e.fakePlayer.PlayStream(ctx, stream)
}

type blockedGenerationTTS struct {
	fakeTTS
	entered chan struct{}
	release chan struct{}
}

func (p *blockedGenerationTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	close(p.entered)
	<-p.release // Models a provider whose synchronous call ignores cancellation.
	return p.fakeTTS.Synthesize(ctx, text)
}

func TestOutputResetFencesSynthesisBeforePlaybackAdmission(t *testing.T) {
	engine := &generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}
	defer engine.Close()
	provider := &blockedGenerationTTS{entered: make(chan struct{}), release: make(chan struct{})}
	p := &Pipeline{Player: engine, TTS: provider}
	done := make(chan bool, 1)
	go func() {
		done <- p.synthesizeSegment(context.Background(), nil, "hello", 0, &atomic.Bool{}, make(chan playbackEvent, 10), nil)
	}()
	<-provider.entered
	if err := p.ResetAudioOutput(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	if <-done {
		t.Fatal("pre-reset synthesis enqueued playback")
	}
	engine.mu.Lock()
	accepted := engine.accepted
	engine.mu.Unlock()
	if accepted != 0 {
		t.Fatalf("accepted stale streams: %d", accepted)
	}
	// A fresh explicit attempt can use the same engine and provider binding.
	ctx := audio.BindPlaybackContext(t.Context(), engine)
	generation, _ := audio.PlaybackGeneration(ctx, engine)
	if generation != 1 {
		t.Fatalf("fresh generation = %d", generation)
	}
}

func TestOutputResetRevokesCompletionWithoutConsumingPending(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	p.Player = &generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}
	defer p.Player.Close()
	err := p.withCompletionLease(t.Context(), func(ctx context.Context, lease work.CompletionLease, _ []work.ResultView) error {
		if err := p.ResetAudioOutput(ctx); err != nil {
			return err
		}
		if lease.Valid() {
			t.Error("reset left completion lease valid")
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(c.Pending(scope)) == 0 {
		t.Fatal("reset consumed pending completion")
	}
}

type resetWhileListeningSTT struct {
	fakeSTT
	listening chan struct{}
	events    chan stt.Event
}

func (s *resetWhileListeningSTT) Start(context.Context) (stt.Session, error) {
	close(s.listening)
	return &fakeSTTSession{events: s.events}, nil
}

func TestOutputResetWhileListeningAdmitsFreshUtterance(t *testing.T) {
	engine := &generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}
	defer engine.Close()
	provider := &resetWhileListeningSTT{listening: make(chan struct{}), events: make(chan stt.Event, 1)}
	p := &Pipeline{Player: engine, STT: provider, TTS: &fakeTTS{}, Brain: &fakeBrain{chunks: []string{"The freshly spoken reply is here."}}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.RunTurn(ctx); done <- err }()
	<-provider.listening
	if err := p.ResetAudioOutput(ctx); err != nil {
		t.Fatal(err)
	}
	provider.events <- stt.FinalTranscript{Text: "fresh input"}
	close(provider.events)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	accepted := engine.accepted
	engine.mu.Unlock()
	if accepted == 0 {
		t.Fatal("reset during listening rejected fresh response")
	}
}
