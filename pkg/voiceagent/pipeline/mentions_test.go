package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestAgentMentionIsVisibleButNeverSynthesized(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		t.Run(mode, func(t *testing.T) {
			// Split the control across model tokens as a real provider can.
			chunks := []string{"Sure thing, Lance.\n", "@", "Ven", "essa", " Hey Venessa! What should we cover?"}
			provider := &groupingFakeTTS{}
			var response string
			if mode == "progressive" {
				_, response = runScript(t, provider, chunks...)
			} else {
				player := newFakePlayer(time.Millisecond)
				defer player.Close()
				bus := events.NewBus()
				bus.SubscribeAll(func(e events.Event) {
					if ready, ok := e.(events.ResponseReady); ok {
						response = ready.Response
					}
				})
				p := &Pipeline{Brain: &continuationBrain{chunks: chunks}, TTS: provider, Player: player, Events: bus}
				if err := p.RunTurnTextMode(t.Context(), "Talk with Venessa."); err != nil {
					t.Fatal(err)
				}
			}
			spoken := strings.Join(provider.Texts(), " ")
			if strings.Contains(spoken, "@") || strings.Count(spoken, "Venessa") != 1 || !strings.Contains(spoken, "What should we cover?") {
				t.Fatalf("TTS received %q", spoken)
			}
			if !strings.Contains(response, "@Venessa Hey Venessa!") {
				t.Fatalf("transcript lost the routing control: %q", response)
			}
		})
	}
}

func TestMentionOnlyResponseDoesNotSynthesize(t *testing.T) {
	provider := &groupingFakeTTS{}
	calls, response := runScript(t, provider, "@Venessa")
	if len(calls) != 0 || response != "@Venessa" || speakableFromResponse("@Venessa") != "" {
		t.Fatalf("calls=%q response=%q", calls, response)
	}
}
