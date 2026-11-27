package pipeline

import (
	"context"
	"errors"
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
	if n := metrics.speechInFlightCount(); n != 0 {
		t.Fatalf("speech in flight after the turn = %d", n)
	}
}

func TestDeliberateStopsBeforeSpeechStillSilenceReply(t *testing.T) {
	for _, source := range []string{"text_submit", "talk_key", "escape", "desktop", "unspecified"} {
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

// replyStream is a model reply the test feeds one chunk at a time.
type replyStream struct {
	t      *testing.T
	ctx    context.Context
	chunks chan string
	done   chan brain.StreamResult
}

func newReplyStream(t *testing.T, ctx context.Context) *replyStream {
	return &replyStream{t: t, ctx: ctx, chunks: make(chan string), done: make(chan brain.StreamResult, 1)}
}

func (r *replyStream) stream() *brain.Stream {
	return &brain.Stream{Chunks: r.chunks, Done: r.done}
}

func (r *replyStream) send(chunk string) {
	r.t.Helper()
	select {
	case r.chunks <- chunk:
	case <-r.ctx.Done():
		r.t.Fatal("reply stream stalled")
	}
}

func (r *replyStream) finish() {
	close(r.chunks)
	r.done <- brain.StreamResult{}
	close(r.done)
}

func waitFor(t *testing.T, ctx context.Context, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(what)
	}
}

func signalOn[E events.Event](bus *events.Bus) <-chan struct{} {
	ch := make(chan struct{}, 1)
	events.Subscribe(bus, func(E) {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	return ch
}

type streamOutcome struct {
	text        string
	interrupted bool
	err         error
}

func runReply(p *Pipeline, ctx context.Context, stream *brain.Stream, metrics *turnMetrics) <-chan streamOutcome {
	finished := make(chan streamOutcome, 1)
	go func() {
		text, interrupted, err := p.streamResponse(ctx, nil, stream, false, metrics, p.newTurnConductor(metrics), nil)
		finished <- streamOutcome{text, interrupted, err}
	}()
	return finished
}

func awaitReply(t *testing.T, ctx context.Context, finished <-chan streamOutcome) streamOutcome {
	t.Helper()
	select {
	case result := <-finished:
		return result
	case <-ctx.Done():
		t.Fatal("turn did not finish")
	}
	return streamOutcome{}
}

func TestKeystrokeDuringSpeechSilencesRestOfReply(t *testing.T) {
	p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
	release := engine.HoldPlayback()
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = audio.BindPlaybackContext(ctx, engine)
	speaking := signalOn[events.SpeakingStarted](p.Events)
	reply := newReplyStream(t, ctx)
	finished := runReply(p, ctx, reply.stream(), metrics)
	reply.send("The first sentence is long enough to stand on its own.\n\n")
	waitFor(t, ctx, speaking, "first sentence was not spoken")
	p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	reply.send("A later sentence in the same reply.\n\n")
	reply.finish()
	result := awaitReply(t, ctx, finished)
	if result.err != nil || !result.interrupted || !metrics.interrupted {
		t.Fatalf("interrupted=%v err=%v", result.interrupted, result.err)
	}
	if engine.acceptedCount() != 1 {
		t.Fatalf("accepted %d playbacks, want only the first sentence", engine.acceptedCount())
	}
	want := events.SpeechStop{Source: StopSourceKeystroke, Count: 1, KeyRunes: 1}
	if got := metrics.snapshot().SpeechStop; got == nil || *got != want {
		t.Fatalf("speech stop = %+v, want %+v", got, want)
	}
}

func TestKeystrokeWhileWorkingSilentlyAfterALineLeavesAnswerSpoken(t *testing.T) {
	p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = audio.BindPlaybackContext(ctx, engine)
	spoken := signalOn[events.SpeakingComplete](p.Events)
	reply := newReplyStream(t, ctx)
	finished := runReply(p, ctx, reply.stream(), metrics)
	reply.send("Let me look that up for you.\n\n")
	waitFor(t, ctx, spoken, "opening line was not spoken")
	p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	reply.send("Here is the answer you asked for.\n\n")
	reply.finish()
	result := awaitReply(t, ctx, finished)
	if result.err != nil || result.interrupted || metrics.interrupted {
		t.Fatalf("interrupted=%v err=%v", result.interrupted, result.err)
	}
	if engine.acceptedCount() != 2 {
		t.Fatalf("accepted %d playbacks, want the line and the answer", engine.acceptedCount())
	}
	if got := metrics.snapshot().SpeechStop; got != nil {
		t.Fatalf("ignored keystroke reached the metrics: %+v", got)
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

// The player is idle between two sentences while the second is still being
// synthesized; that speech is in flight, so a keystroke silences it.
func TestKeystrokeWhileNextSentenceSynthesizesSilencesIt(t *testing.T) {
	provider := &secondSentenceTTS{entered: make(chan struct{}), release: make(chan struct{})}
	p, engine, metrics := newStopTestPipeline(t, provider)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = audio.BindPlaybackContext(ctx, engine)
	firstDone := signalOn[events.SpeakingComplete](p.Events)
	reply := newReplyStream(t, ctx)
	finished := runReply(p, ctx, reply.stream(), metrics)
	reply.send("The first sentence is long enough to stand on its own.\n\n")
	waitFor(t, ctx, firstDone, "first sentence was not spoken")
	reply.send("The second sentence arrives after a pause.\n\n")
	waitFor(t, ctx, provider.entered, "second sentence never reached synthesis")
	if engine.IsPlaying() {
		t.Fatal("player still busy with the first sentence")
	}
	p.StopPlaybackFrom(StopSource{Name: StopSourceKeystroke, KeyRunes: 1})
	close(provider.release)
	reply.finish()
	result := awaitReply(t, ctx, finished)
	if result.err != nil || !result.interrupted || !metrics.interrupted {
		t.Fatalf("interrupted=%v err=%v", result.interrupted, result.err)
	}
	if engine.acceptedCount() != 1 {
		t.Fatalf("accepted %d playbacks, want only the first sentence", engine.acceptedCount())
	}
	want := events.SpeechStop{Source: StopSourceKeystroke, Count: 1, KeyRunes: 1}
	if got := metrics.snapshot().SpeechStop; got == nil || *got != want {
		t.Fatalf("speech stop = %+v, want %+v", got, want)
	}
}

// activeMetricsTTS remembers the turn whose speech it was asked to make.
type activeMetricsTTS struct {
	tts.Provider
	p       *Pipeline
	mu      sync.Mutex
	metrics *turnMetrics
}

func (a *activeMetricsTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	a.p.activeMu.Lock()
	m := a.p.activeMetrics
	a.p.activeMu.Unlock()
	a.mu.Lock()
	a.metrics = m
	a.mu.Unlock()
	return a.Provider.Synthesize(ctx, text)
}

func (a *activeMetricsTTS) seen() *turnMetrics {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.metrics
}

func TestSpeechInFlightReturnsToZero(t *testing.T) {
	check := func(t *testing.T, metrics *turnMetrics) {
		t.Helper()
		if metrics == nil {
			t.Fatal("no turn reached synthesis")
		}
		if n := metrics.speechInFlightCount(); n != 0 {
			t.Fatalf("speech in flight after the turn = %d", n)
		}
	}
	t.Run("reply synthesis error", func(t *testing.T) {
		p, engine, metrics := newStopTestPipeline(t, &failingTTS{err: errors.New("synthesis failed")})
		ctx := audio.BindPlaybackContext(t.Context(), engine)
		if _, _, err := p.streamResponse(ctx, nil, scriptedStream(ctx, []string{"One sentence. And another one after it."}, nil), false, metrics, p.newTurnConductor(metrics), nil); err != nil {
			t.Fatal(err)
		}
		check(t, metrics)
	})
	t.Run("deliberate stop while speaking", func(t *testing.T) {
		p, engine, metrics := newStopTestPipeline(t, &fakeTTS{})
		release := engine.HoldPlayback()
		defer release()
		speaking := signalOn[events.SpeakingStarted](p.Events)
		go func() {
			<-speaking
			p.StopPlaybackFrom(StopSource{Name: "text_submit"})
		}()
		ctx := audio.BindPlaybackContext(t.Context(), engine)
		_, interrupted, err := p.streamResponse(ctx, nil, scriptedStream(ctx, []string{"A reply cut off mid sentence."}, nil), false, metrics, p.newTurnConductor(metrics), nil)
		if err != nil || !interrupted {
			t.Fatalf("interrupted=%v err=%v", interrupted, err)
		}
		check(t, metrics)
	})
	t.Run("model fails while speech is queued", func(t *testing.T) {
		provider := &blockedGenerationTTS{entered: make(chan struct{}), release: make(chan struct{})}
		p, engine, metrics := newStopTestPipeline(t, provider)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		ctx = audio.BindPlaybackContext(ctx, engine)
		reply := newReplyStream(t, ctx)
		finished := runReply(p, ctx, reply.stream(), metrics)
		reply.send("The first sentence is long enough to stand on its own.\n\n")
		waitFor(t, ctx, provider.entered, "sentence never reached synthesis")
		close(reply.chunks)
		reply.done <- brain.StreamResult{Err: errors.New("model failed")}
		close(reply.done)
		if result := awaitReply(t, ctx, finished); result.err == nil {
			t.Fatal("model failure was not reported")
		}
		close(provider.release)
		check(t, metrics)
	})
	t.Run("standalone line", func(t *testing.T) {
		for _, provider := range []tts.Provider{&fakeTTS{}, &failingTTS{err: errors.New("synthesis failed")}} {
			p, _, metrics := newStopTestPipeline(t, provider)
			_ = p.speakStandalone(t.Context(), nil, metrics, "Starting that now.")
			check(t, metrics)
		}
	})
	t.Run("typed reply", func(t *testing.T) {
		for _, provider := range []tts.Provider{&fakeTTS{}, &failingTTS{err: errors.New("synthesis failed")}} {
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{Brain: &fakeBrain{chunks: []string{"A typed reply to speak."}}, Player: player, Events: events.NewBus()}
			recorder := &activeMetricsTTS{Provider: provider, p: p}
			p.TTS = recorder
			if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
				t.Fatal(err)
			}
			check(t, recorder.seen())
		}
	})
}
