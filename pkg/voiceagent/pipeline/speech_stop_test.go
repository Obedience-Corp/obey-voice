package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type stopGenerationEngine struct{ *generationTestEngine }

func (e *stopGenerationEngine) Stop() {
	e.mu.Lock()
	e.generation++
	e.mu.Unlock()
	e.fakePlayer.Stop()
}

func TestTypingStopsTurnSpeechButRetainsReplyText(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "during synthesis", true: "during thinking"}[before], func(t *testing.T) {
			engine := &stopGenerationEngine{&generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}}
			defer engine.Close()
			provider := &blockedGenerationTTS{entered: make(chan struct{}), release: make(chan struct{})}
			bus := events.NewBus()
			var failures atomic.Int32
			events.Subscribe(bus, func(events.Error) { failures.Add(1) })
			p := &Pipeline{Player: engine, TTS: provider, Events: bus}
			metrics := newTurnMetrics()
			clear := p.trackMetrics(metrics)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// Bind before the stop, just as voice input does before waiting for
			// the first model output. Stop must never re-admit this old binding.
			ctx = audio.BindPlaybackContext(ctx, engine)
			if before {
				p.StopPlayback()
			}
			const reply = "The first sentence is long enough. The second sentence follows it."
			type outcome struct {
				text        string
				interrupted bool
				err         error
			}
			finished := make(chan outcome, 1)
			go func() {
				text, interrupted, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{reply}, nil), false, metrics, p.newTurnConductor(metrics), nil)
				finished <- outcome{text, interrupted, err}
			}()
			if !before {
				select {
				case <-provider.entered:
				case <-ctx.Done():
					t.Fatal("synthesis did not start")
				}
				p.StopPlayback()
				close(provider.release)
			}
			select {
			case result := <-finished:
				if result.err != nil || result.text != reply || !result.interrupted || !metrics.interrupted || metrics.degraded {
					t.Fatalf("stopped output lost text or reported failure: %+v metrics=%+v", result, metrics.snapshot())
				}
			case <-ctx.Done():
				t.Fatal("stopped speech held the turn open")
			}
			clear()
			if failures.Load() != 0 {
				t.Fatal("intentional output stop emitted errors")
			}
			engine.mu.Lock()
			accepted := engine.accepted
			engine.mu.Unlock()
			if accepted != 0 {
				t.Fatal("stopped turn admitted late audio")
			}
			// The next turn owns a fresh output context and can speak normally.
			p.TTS = &fakeTTS{}
			next := newTurnMetrics()
			_, interrupted, err := p.streamResponse(t.Context(), nil, scriptedStream(t.Context(), []string{"A fresh reply is audible."}, nil), false, next, p.newTurnConductor(next), nil)
			if err != nil || interrupted || next.firstAudioReady.IsZero() {
				t.Fatalf("next turn stayed silent: interrupted=%v err=%v", interrupted, err)
			}
		})
	}
}

type canceledSpeechTTS struct {
	fakeTTS
	started chan struct{}
}

func (p *canceledSpeechTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunawayModelErrorCancelsQueuedSpeech(t *testing.T) {
	for _, cause := range []error{brain.ErrResponseLimit, brain.ErrResponseRepetition} {
		t.Run(cause.Error(), func(t *testing.T) {
			provider := &canceledSpeechTTS{started: make(chan struct{})}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
			var playbackErrors atomic.Int32
			events.Subscribe(p.Events, func(e events.Error) {
				if e.Stage == "playback" || e.Stage == "tts" {
					playbackErrors.Add(1)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			chunks := make(chan string, 1)
			done := make(chan brain.StreamResult, 1)
			chunks <- "The useful prefix should remain visible. "
			go func() {
				select {
				case <-provider.started:
					done <- brain.StreamResult{Err: cause, Recovered: true}
					close(chunks)
					close(done)
				case <-ctx.Done():
				}
			}()
			metrics := newTurnMetrics()
			text, interrupted, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, p.newTurnConductor(metrics), nil)
			if err != nil || interrupted || !metrics.degraded || !strings.Contains(text, "useful prefix") || playbackErrors.Load() != 0 {
				t.Fatalf("runaway did not drain quietly: text=%q interrupted=%v err=%v degraded=%v errors=%d", text, interrupted, err, metrics.degraded, playbackErrors.Load())
			}
		})
	}
}

func TestPlaybackFailureMarksTurnDegraded(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	failure := synthesisFailureEvent("hello", 0, synthesisObservation{}, errors.New("device unavailable"))
	p.applyPlaybackEvent(*failure, metrics, new(atomic.Int64))
	if !metrics.degraded {
		t.Fatal("failed output was presented as successful completion")
	}
}
