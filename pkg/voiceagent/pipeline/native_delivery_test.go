package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestNativeReplyWaitsForSuccessfulTerminalBeforeSpeech(t *testing.T) {
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	provider := &groupingFakeTTS{}
	bus := events.NewBus()
	received := make(chan events.ResponseDelta, 8)
	events.Subscribe(bus, func(event events.ResponseDelta) { received <- event })
	pipeline := &Pipeline{Native: &NativePolicy{}, TTS: provider, Player: player, Events: bus}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	chunks := make(chan string, 1)
	terminal := make(chan brain.StreamResult, 1)
	chunks <- "The answer is fifteen."
	close(chunks)
	finished := make(chan error, 1)
	go func() {
		metrics := newTurnMetrics()
		_, _, err := pipeline.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: terminal}, false, metrics, pipeline.newTurnConductor(metrics), nil)
		finished <- err
	}()
	for {
		select {
		case <-received:
			goto displayed
		case <-ctx.Done():
			t.Fatal("no live display before terminal")
		}
	}
displayed:
	if len(provider.Texts()) != 0 {
		t.Fatal("speech began before terminal")
	}
	terminal <- brain.StreamResult{}
	close(terminal)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if len(provider.Texts()) != 1 {
		t.Fatal(provider.Texts())
	}
}
func TestNativeFailedOrOversizedReplyNeverSynthesizesPartial(t *testing.T) {
	for _, test := range []struct {
		name, text string
		result     brain.StreamResult
		want       error
	}{
		{"failure", "A partial reply.", brain.StreamResult{Err: errors.New("model failed")}, nil},
		{"recovered_failure", "A recovered partial.", brain.StreamResult{Err: errors.New("model failed"), Recovered: true}, nil},
		{"overflow", strings.Repeat("x", nativeReplyLimit+1), brain.StreamResult{}, ErrNativeReplyLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			provider := &groupingFakeTTS{}
			pipeline := &Pipeline{Native: &NativePolicy{}, TTS: provider, Player: player, Events: events.NewBus()}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			chunks := make(chan string, 1)
			chunks <- test.text
			close(chunks)
			terminal := make(chan brain.StreamResult, 1)
			terminal <- test.result
			close(terminal)
			metrics := newTurnMetrics()
			_, _, err := pipeline.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: terminal}, false, metrics, pipeline.newTurnConductor(metrics), nil)
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatal(err)
			}
			if len(provider.Texts()) != 0 {
				t.Fatal("synthesized failed partial", provider.Texts())
			}
		})
	}
}
