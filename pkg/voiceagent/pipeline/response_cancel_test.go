package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestStreamResponseCancellationAfterDrain(t *testing.T) {
	for _, allowBargeIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "voice"}[allowBargeIn], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bus := events.NewBus()
			p := &Pipeline{Events: bus}
			metrics := newTurnMetrics()
			turn := p.newTurnConductor(metrics)
			// A stream without a terminal result emits ThinkingComplete after
			// the input/playback loop drains. Cancel at that boundary, so this
			// exercises late cancellation without depending on select ordering.
			chunks := make(chan string, 1)
			chunks <- "Retain the partial response."
			close(chunks)
			done := make(chan brain.StreamResult)
			close(done)
			completed := 0
			events.Subscribe(bus, func(events.ThinkingComplete) {
				completed++
				cancel()
			})
			response, interrupted, err := p.streamResponse(ctx, nil, &brain.Stream{Chunks: chunks, Done: done}, allowBargeIn, metrics, turn, nil)
			if completed != 1 || response != "Retain the partial response." {
				t.Fatalf("completion events=%d response=%q", completed, response)
			}
			if interrupted {
				t.Fatal("external cancellation must not invent a microphone barge-in")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("late cancellation error = %v, want context.Canceled", err)
			}
		})
	}
}
