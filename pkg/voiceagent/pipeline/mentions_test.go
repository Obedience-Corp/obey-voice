package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func runMentionReply(t *testing.T, mode string, lookup func(string) (string, bool), chunks ...string) ([]string, string) {
	t.Helper()
	provider := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{STT: &fakeSTT{text: "What should we cover?"}, Brain: &continuationBrain{chunks: chunks}, TTS: provider, Player: player, Events: events.NewBus()}
	if lookup != nil {
		p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
			return ResponseAdmission{Request: input, AgentMentionName: lookup}, nil
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

// mentionName plays a roster with two personas, one of them also known by ID.
func mentionName(target string) (string, bool) {
	switch strings.ToLower(target) {
	case "venessa", "p-venessa":
		return "Venessa", true
	case "milo":
		return "Milo", true
	}
	return "", false
}

func TestInvitedPersonasAreSpokenByNameWithoutTheMarker(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		for _, tc := range []struct{ name, reply, want string }{
			{"one", "@Milo, what do you think?", "Milo, what do you think?"},
			{"several", "@Milo @Venessa, what do you two think?", "Milo, Venessa, what do you two think?"},
			{"by id at the end", "What do you make of that, @p-venessa?", "What do you make of that, Venessa?"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				calls, response := runMentionReply(t, mode, mentionName, tc.reply)
				if spoken := strings.Join(calls, " "); spoken != tc.want {
					t.Fatalf("TTS received %q, want %q", spoken, tc.want)
				}
				if response != tc.reply {
					t.Fatalf("transcript lost the routing control: %q", response)
				}
			})
		}
	}
}

func TestAgentMentionBesideItsNameIsSpokenOnce(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		t.Run(mode, func(t *testing.T) {
			// Split the control across model tokens as a real provider can.
			chunks := []string{"Sure thing, Lance.\n", "@", "Ven", "essa", " Hey Venessa! What should we cover?"}
			calls, response := runMentionReply(t, mode, mentionName, chunks...)
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

func TestMentionOnlyResponseSpeaksTheName(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		t.Run(mode, func(t *testing.T) {
			calls, response := runMentionReply(t, mode, mentionName, "@Venessa")
			if strings.Join(calls, " ") != "Venessa" || response != "@Venessa" {
				t.Fatalf("calls=%q response=%q", calls, response)
			}
		})
	}
}

func TestUnrecognizedMentionsRemainSpoken(t *testing.T) {
	for _, mode := range []string{"progressive", "text"} {
		for _, tc := range []struct {
			name, reply, want string
			lookup            func(string) (string, bool)
		}{
			{"without_roster", "Contact @support for help with @typescript.", "Contact @support for help with @typescript.", nil},
			{"unknown_handles", "Contact @support for help with @typescript.", "Contact @support for help with @typescript.", mentionName},
			{"unknown_only", "@support", "@support", mentionName},
			{"mixed", "@Venessa, contact @support for help.", "Venessa, contact @support for help.", mentionName},
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
