package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestFailedPromiseRepairRoutesSpawnWithoutTTS(t *testing.T) {
	const promise = "I am invoking the tool now to retrieve only those records."

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	var routed RouteDecision
	var routedMu sync.Mutex

	p := &Pipeline{
		Brain:                  &fakeBrain{chunks: []string{promise}},
		TTS:                    provider,
		Player:                 player,
		Events:                 events.NewBus(),
		VoiceFailedRouteRepair: true,
		OnRoute: func(d RouteDecision) {
			routedMu.Lock()
			routed = d
			routedMu.Unlock()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	repair := &promiseRepairState{enabled: true}
	stream, err := p.Brain.ThinkStream(ctx, "filter design work items", brain.StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream: %v", err)
	}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	_, _, err = p.streamResponse(ctx, cancel, stream, false, metrics, turn, repair)
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if !repair.spawn {
		t.Fatal("repair.spawn = false, want true")
	}

	for _, s := range provider.Calls() {
		if strings.Contains(strings.ToLower(s), "invoking the tool") {
			t.Fatalf("promise was spoken: %v", provider.Calls())
		}
	}

	p.completeRoutedTurn(ctx, turn, metrics, "filter design work items", RouteSpawn, "repair")
	routedMu.Lock()
	defer routedMu.Unlock()
	if routed.Kind != RouteSpawn || routed.Source != "repair" {
		t.Fatalf("OnRoute = %+v, want spawn/repair", routed)
	}
}

func TestLayer1SpawnSkipsThinkStream(t *testing.T) {
	brainProvider := &countingFakeBrain{fakeBrain: fakeBrain{chunks: []string{"should not run"}}}
	p := &Pipeline{
		Brain:  brainProvider,
		Events: events.NewBus(),
		OnRoute: func(d RouteDecision) {
			if d.Kind != RouteNarrate {
				t.Fatalf("route kind = %q, want narrate", d.Kind)
			}
		},
	}

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	kind, ok := classifyPhrase("tell me a long story", p.routeContext())
	if !ok || !routeKindSkipsBrain(kind) {
		t.Fatal("expected narrate phrase match")
	}
	p.completeRoutedTurn(context.Background(), turn, metrics, "tell me a long story", kind, "phrase")

	if brainProvider.thinkCalls != 0 {
		t.Fatalf("ThinkStream called %d times, want 0", brainProvider.thinkCalls)
	}
}

type countingFakeBrain struct {
	fakeBrain
	thinkCalls int
}

func (c *countingFakeBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	c.thinkCalls++
	return c.fakeBrain.ThinkStream(ctx, input, opts)
}
