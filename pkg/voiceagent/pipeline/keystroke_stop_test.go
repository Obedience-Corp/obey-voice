package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func newStopTestPipeline(t *testing.T, provider tts.Provider) (*Pipeline, *stopGenerationEngine, *turnMetrics) {
	t.Helper()
	engine := &stopGenerationEngine{&generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}}
	t.Cleanup(func() { _ = engine.Close() })
	p := &Pipeline{Player: engine, TTS: provider, Events: events.NewBus(), idleFlush: time.Millisecond, openingHold: time.Millisecond}
	metrics := newTurnMetrics()
	t.Cleanup(p.trackMetrics(metrics))
	return p, engine, metrics
}

func (e *stopGenerationEngine) acceptedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.accepted
}

func TestKeystrokeBeforeSpeechLeavesReplySpoken(t *testing.T) {
	p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
	ctx := audio.BindPlaybackContext(t.Context(), engine)
	p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	const reply = "A reply that should be heard."
	text, interrupted, err := p.streamResponse(ctx, nil, scriptedStream(ctx, []string{reply}, nil), false, metrics, p.newTurnConductor(metrics), nil)
	if err != nil || interrupted || metrics.interrupted || text != reply {
		t.Fatalf("text=%q interrupted=%v metrics.interrupted=%v err=%v", text, interrupted, metrics.interrupted, err)
	}
	if engine.acceptedCount() != 1 || metrics.playbackStart.IsZero() {
		t.Fatalf("reply was not played: accepted=%d", engine.acceptedCount())
	}
	if got := metrics.snapshot(); got.SpeechStop != nil || got.BargeInElapsed != 0 {
		t.Fatalf("ignored keystroke reached the metrics: stop=%+v barge-in=%v", got.SpeechStop, got.BargeInElapsed)
	}
}

func TestDeliberateStopsBeforeSpeechStillSilenceReply(t *testing.T) {
	for _, source := range []string{"text_submit", "talk_key", "desktop", "unspecified"} {
		t.Run(source, func(t *testing.T) {
			p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
			ctx := audio.BindPlaybackContext(t.Context(), engine)
			p.StopPlaybackFrom(StopSource{Name: source})
			_, interrupted, err := p.streamResponse(ctx, nil, scriptedStream(ctx, []string{"A reply nobody hears."}, nil), false, metrics, p.newTurnConductor(metrics), nil)
			if err != nil || !interrupted {
				t.Fatalf("interrupted=%v err=%v", interrupted, err)
			}
			if engine.acceptedCount() != 0 {
				t.Fatal("stopped reply was played")
			}
			want := events.SpeechStop{Source: source, Count: 1, BeforeAudio: true}
			if got := metrics.snapshot().SpeechStop; got == nil || *got != want {
				t.Fatalf("speech stop = %+v, want %+v", got, want)
			}
		})
	}
}

func TestKeystrokeDuringSpeechSilencesReply(t *testing.T) {
	p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
	release := engine.HoldPlayback()
	defer release()
	speaking := make(chan struct{}, 1)
	events.Subscribe(p.Events, func(events.SpeakingStarted) {
		select {
		case speaking <- struct{}{}:
		default:
		}
	})
	go func() {
		<-speaking
		p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	}()
	_, interrupted, err := p.streamResponse(t.Context(), nil, scriptedStream(t.Context(), []string{"A reply cut off mid sentence."}, nil), false, metrics, p.newTurnConductor(metrics), nil)
	if err != nil || !interrupted || !metrics.interrupted {
		t.Fatalf("interrupted=%v err=%v", interrupted, err)
	}
	want := events.SpeechStop{Source: StopSourceKeystroke, Count: 1, KeyRunes: 1}
	if got := metrics.snapshot().SpeechStop; got == nil || *got != want {
		t.Fatalf("speech stop = %+v, want %+v", got, want)
	}
}

// secondSentenceTTS holds the reply's second synthesis until released.
type secondSentenceTTS struct {
	fakeTTS
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (s *secondSentenceTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	s.mu.Lock()
	s.calls++
	second := s.calls == 2
	s.mu.Unlock()
	if second {
		close(s.entered)
		<-s.release
	}
	return s.fakeTTS.Synthesize(ctx, text)
}

func TestKeystrokeBetweenSentencesSilencesRestOfReply(t *testing.T) {
	provider := &secondSentenceTTS{entered: make(chan struct{}), release: make(chan struct{})}
	p, engine, metrics := newStopTestPipeline(t, provider)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = audio.BindPlaybackContext(ctx, engine)
	firstDone := make(chan struct{}, 1)
	events.Subscribe(p.Events, func(events.SpeakingComplete) {
		select {
		case firstDone <- struct{}{}:
		default:
		}
	})
	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	type outcome struct {
		interrupted bool
		err         error
	}
	finished := make(chan outcome, 1)
	go func() {
		_, interrupted, err := p.streamResponse(ctx, nil, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, p.newTurnConductor(metrics), nil)
		finished <- outcome{interrupted, err}
	}()
	send := func(chunk string) {
		select {
		case chunks <- chunk:
		case <-ctx.Done():
			t.Fatal("reply stream stalled")
		}
	}
	wait := func(ch <-chan struct{}, what string) {
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(what)
		}
	}
	send("The first sentence is long enough to stand on its own.\n\n")
	wait(firstDone, "first sentence was not spoken")
	send("The second sentence arrives after a pause.\n\n")
	wait(provider.entered, "second sentence never reached synthesis")
	if engine.IsPlaying() {
		t.Fatal("player still busy with the first sentence")
	}
	p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	close(provider.release)
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	select {
	case result := <-finished:
		if result.err != nil || !result.interrupted || !metrics.interrupted {
			t.Fatalf("interrupted=%v err=%v", result.interrupted, result.err)
		}
	case <-ctx.Done():
		t.Fatal("turn did not finish")
	}
	if engine.acceptedCount() != 1 {
		t.Fatalf("accepted %d playbacks, want only the first sentence", engine.acceptedCount())
	}
	want := events.SpeechStop{Source: StopSourceKeystroke, Count: 1, KeyRunes: 1}
	if got := metrics.snapshot().SpeechStop; got == nil || *got != want {
		t.Fatalf("speech stop = %+v, want %+v", got, want)
	}
}
