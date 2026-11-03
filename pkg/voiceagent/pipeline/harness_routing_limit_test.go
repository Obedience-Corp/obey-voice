package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestHarnessRoutingLimitAllowsTextVoiceAndFollowingConversation(t *testing.T) {
	for _, voice := range []bool{false, true} {
		for _, largeHistory := range []bool{false, true} {
			for _, tiered := range []bool{false, true} {
				t.Run(fmt.Sprintf("voice=%t/history=%t/tiered=%t", voice, largeHistory, tiered), func(t *testing.T) {
					p, b, jobs := harnessPipeline(t, tiered)
					document := strings.Repeat("Document paragraph.\n", 8192)
					input := "Discuss this document:\n" + document
					if largeHistory {
						b.turns = append(b.turns, brain.Turn{Role: "user", Content: document})
						input = "Summarize the document above."
					}
					before := b.History()
					b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
						return brain.StreamResult{}, fmt.Errorf("routing input: %w", brain.ErrHarnessRouteContextLimit)
					}
					var replies []string
					var dispositions []string
					var failures []events.Error
					events.Subscribe(p.Events, func(e events.ResponseReady) { replies = append(replies, e.Response) })
					events.Subscribe(p.Events, func(e events.DecisionEvaluated) { dispositions = append(dispositions, e.Disposition) })
					events.Subscribe(p.Events, func(e events.Error) { failures = append(failures, e) })
					inputs := []string{input, "What about the second paragraph?"}
					for _, input := range inputs {
						var err error
						if voice {
							p.STT = &fakeSTT{text: input}
							_, err = p.RunTurn(t.Context())
						} else {
							err = p.RunTurnTextMode(t.Context(), input)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					if !reflect.DeepEqual(b.chatInputs, inputs) || !reflect.DeepEqual(b.History(), before) {
						t.Fatal("fallback changed input or discarded history")
					}
					if len(replies) != 2 || replies[0] != "Conversation answer." || replies[1] != "Conversation answer." || len(failures) != 0 {
						t.Fatalf("replies=%q errors=%+v", replies, failures)
					}
					if len(jobs.Status()) != 0 {
						t.Fatal("routing limit admitted work")
					}
					if !reflect.DeepEqual(dispositions, []string{"context_limit", "context_limit"}) {
						t.Fatalf("dispositions=%v", dispositions)
					}
					for _, opts := range b.chat {
						if opts.HarnessRouting == nil || !opts.HarnessRouting.ContextLimited || opts.RouterToolsPolicy != brain.RouterToolsRouteOnly {
							t.Fatal("fallback lost routing-limit guidance or changed execution policy")
						}
					}
				})
			}
		}
	}
}

func TestHarnessRoutingLimitDoesNotBypassCancellationOrPersonaFence(t *testing.T) {
	for _, mode := range []string{"cancel", "persona change"} {
		t.Run(mode, func(t *testing.T) {
			p, b, jobs := harnessPipeline(t, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
				if mode == "cancel" {
					cancel()
				} else {
					p.Brain = &fakeBrain{}
				}
				return brain.StreamResult{}, brain.ErrHarnessRouteContextLimit
			}
			_, err := p.startBrainStream(ctx, "Discuss this document", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly})
			if err == nil || len(b.chat) != 0 || len(jobs.Status()) != 0 {
				t.Fatal("context-limit recovery bypassed the turn fence")
			}
		})
	}
}
