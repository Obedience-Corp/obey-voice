package brain

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// A warmup that reaches the runner after a turn's first request runs between
// that request and the next one, delaying the reply by a full prefill.
func TestWarmupAfterATurnStartedSendsNothing(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", fullSystemPrompt: "You are Samantha.",
		cfg: &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, MaxHistory: 10}}
	stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	turn := count

	o.Warmup(context.Background())

	if count != turn {
		t.Fatalf("warmup after the first turn sent %d request(s)", count-turn)
	}
}
