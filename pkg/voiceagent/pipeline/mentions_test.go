package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func runMentionReply(t *testing.T, mode string, lookup func(string) bool, chunks ...string) ([]string, string) {
	t.Helper()
	provider := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{STT: &fakeSTT{text: "What should we cover?"}, Brain: &continuationBrain{chunks: chunks}, TTS: provider, Player: player, Events: events.NewBus()}
	if lookup != nil {
		p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
			return ResponseAdmission{Request: input, AgentMentionControl: lookup}, nil
		}
	}
	var response string
	events.Subscribe(p.Events, func(e events.ResponseReady) { response = e.Response })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var err error
	if mode == "progressive" {
		_, err = p.RunTurn(ctx)
	} else {
		err = p.RunTurnTextMode(ctx, "What should we cover?")
	}
	if err != nil {
		t.Fatal(err)
	}
	return provider.Texts(), response
}

func isVenessaControl(target string) bool { return strings.EqualFold(target, "Venessa") }

func TestAgentMentionIsVisibleButNeverSynthesized(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		t.Run(mode, func(t *testing.T) {
			// Split the control across model tokens as a real provider can.
			chunks := []string{"Sure thing, Lance.\n", "@", "Ven", "essa", " Hey Venessa! What should we cover?"}
			calls, response := runMentionReply(t, mode, isVenessaControl, chunks...)
			spoken := strings.Join(calls, " ")
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
	for _, mode := range []string{"progressive", "text"} {
		t.Run(mode, func(t *testing.T) {
			calls, response := runMentionReply(t, mode, isVenessaControl, "@Venessa")
			if len(calls) != 0 || response != "@Venessa" {
				t.Fatalf("calls=%q response=%q", calls, response)
			}
		})
	}
}

func TestUnrecognizedMentionsRemainSpoken(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		for _, tc := range []struct {
			name, reply, want string
			lookup            func(string) bool
		}{
			{"without_roster", "Contact @support for help with @typescript.", "Contact @support for help with @typescript.", nil},
			{"unknown_handles", "Contact @support for help with @typescript.", "Contact @support for help with @typescript.", isVenessaControl},
			{"unknown_only", "@support", "@support", isVenessaControl},
			{"mixed", "@Venessa Contact @support for help.", "Contact @support for help.", isVenessaControl},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				calls, response := runMentionReply(t, mode, tc.lookup, tc.reply)
				if spoken := strings.Join(calls, " "); spoken != tc.want {
					t.Fatalf("ordinary handles lost from speech: got %q, want %q", spoken, tc.want)
				}
				if response != tc.reply {
					t.Fatalf("canonical response changed: got %q, want %q", response, tc.reply)
				}
			})
		}
	}
}
