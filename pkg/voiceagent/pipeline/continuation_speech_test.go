package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestPeerToolDiscussionRemainsAudibleWithoutLaunchingWork(t *testing.T) {
	const reply = "I can explain the audiobook feature. I'll run the obey-voice command once you choose a book."
	for _, scenario := range []struct {
		name                string
		continuation, group bool
	}{
		{"peer", true, false},
		{"group_initial", false, true},
		{"group_continuation", true, true},
	} {
		for _, typed := range []bool{false, true} {
			name := "voice"
			if typed {
				name = "typed"
			}
			t.Run(scenario.name+"/"+name, func(t *testing.T) {
				b := &continuationBrain{chunks: []string{reply}}
				tts := &progressiveFakeTTS{}
				player := newFakePlayer(time.Millisecond)
				defer player.Close()
				routes := 0
				p := &Pipeline{STT: &fakeSTT{text: "What do you think?"}, Brain: b, TTS: tts, Player: player, Events: events.NewBus(), VoiceToolsEnabled: true, VoiceFailedRouteRepair: true,
					OnRoute: func(RouteDecision) { routes++ }}
				var final events.ResponseReady
				var metrics events.TurnMetrics
				events.Subscribe(p.Events, func(e events.ResponseReady) { final = e })
				events.Subscribe(p.Events, func(e events.TurnMetrics) { metrics = e })
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				admitted := admission("Venessa")
				if scenario.group {
					state := discussion.New(ctx, "What do you think?", "all", "bounded", []string{"Venessa"})
					defer state.End("test_complete")
					admitted.Discussion = &discussion.Directive{ID: state.ID(), State: state, Initial: !scenario.continuation}
				}
				if scenario.continuation {
					ctx = withContinuation(ctx, continuationRequest{admission: admitted})
				} else {
					p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) { return admitted, nil }
				}
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
				if len(opts) != 1 || opts[0].AgentContinuation != scenario.continuation || opts[0].ToolsEnabled || (opts[0].Discussion != nil) != scenario.group {
					t.Fatalf("peer inference options=%+v", opts)
				}
			})
		}
	}
}
