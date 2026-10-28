package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestPeerToolDiscussionRemainsAudibleWithoutLaunchingWork(t *testing.T) {
	const reply = "I can explain the audiobook feature. I'll run the obey-voice command once you choose a book."
	for _, typed := range []bool{false, true} {
		name := "voice"
		if typed {
			name = "typed"
		}
		t.Run(name, func(t *testing.T) {
			b := &continuationBrain{chunks: []string{reply}}
			tts := &progressiveFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			routes := 0
			p := &Pipeline{Brain: b, TTS: tts, Player: player, Events: events.NewBus(), VoiceToolsEnabled: true, VoiceFailedRouteRepair: true,
				OnRoute: func(RouteDecision) { routes++ }}
			var final events.ResponseReady
			var metrics events.TurnMetrics
			events.Subscribe(p.Events, func(e events.ResponseReady) { final = e })
			events.Subscribe(p.Events, func(e events.TurnMetrics) { metrics = e })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx = withContinuation(ctx, continuationRequest{admission: admission("Venessa")})
			var err error
			if typed {
				err = p.runTextTurn(ctx, "", false)
			} else {
				_, err = p.runTurnOnce(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if routes != 0 || final.Response != reply || final.Degraded {
				t.Fatalf("routes=%d final=%+v", routes, final)
			}
			if got := strings.Join(tts.Calls(), " "); got != reply {
				t.Fatalf("spoken=%q, want %q", got, reply)
			}
			if metrics.Outcome != "completed" || metrics.AudibleSink != "local_speaker" || metrics.PlaybackStartElapsed <= 0 {
				t.Fatalf("metrics=%+v", metrics)
			}
			_, opts := b.calls()
			if len(opts) != 1 || !opts[0].AgentContinuation || opts[0].ToolsEnabled {
				t.Fatalf("peer inference options=%+v", opts)
			}
		})
	}
}
