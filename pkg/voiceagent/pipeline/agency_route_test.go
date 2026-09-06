package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// A Layer-1 turn never reaches ThinkStream, so OnRoute is the only place an
// observer can read the decision. HandleRoute owes exactly one call per
// invocation for every kind — job.status included, and a spawn whose job
// table refused the start — never zero, never two.
func TestHandleRouteNotifiesOnRouteOncePerCall(t *testing.T) {
	cases := []struct {
		name    string
		worker  brain.Provider
		prepare func(t *testing.T, a *Agency, dir string) RouteDecision
	}{
		{
			name:   "spawn",
			worker: &fakeBrain{chunks: []string{"done"}},
			prepare: func(_ *testing.T, _ *Agency, _ string) RouteDecision {
				return RouteDecision{Kind: RouteSpawn, Transcript: "search the web for rust", Source: "intent"}
			},
		},
		{
			name:   "spawn refused by a closed job table",
			worker: &fakeBrain{chunks: []string{"done"}},
			prepare: func(_ *testing.T, a *Agency, _ string) RouteDecision {
				a.jobs.Close()
				return RouteDecision{Kind: RouteSpawn, Transcript: "search the web for rust", Source: "intent"}
			},
		},
		{
			name: "job.status with no job",
			prepare: func(_ *testing.T, _ *Agency, _ string) RouteDecision {
				return RouteDecision{Kind: RouteJobStatus, Transcript: "status", Source: "phrase"}
			},
		},
		{
			name: "job.abort",
			prepare: func(_ *testing.T, _ *Agency, _ string) RouteDecision {
				return RouteDecision{Kind: RouteJobAbort, Transcript: "abort", Source: "phrase"}
			},
		},
		{
			name: "narrate start",
			prepare: func(t *testing.T, a *Agency, dir string) RouteDecision {
				source := filepath.Join(dir, "fixture.txt")
				if err := os.WriteFile(source, []byte("placeholder\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				a.narrator.planSource = func(context.Context, string) ([]narrateUnit, error) {
					return testNarrateUnits("Alpha."), nil
				}
				return RouteDecision{Kind: RouteNarrate, Transcript: "read this " + source, Source: "phrase"}
			},
		},
		{
			name: "narrate start with an unreadable source",
			prepare: func(_ *testing.T, _ *Agency, _ string) RouteDecision {
				return RouteDecision{Kind: RouteNarrate, Transcript: "read this", Source: "phrase"}
			},
		},
		{
			name: "narrate pause with nothing playing",
			prepare: func(_ *testing.T, _ *Agency, _ string) RouteDecision {
				return RouteDecision{Kind: RouteNarratePause, Transcript: "pause", Source: "phrase"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &progressiveFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			dir := t.TempDir()
			jobs := job.NewTable(context.Background())
			defer jobs.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
			var routed []RouteDecision
			p.OnRoute = func(d RouteDecision) { routed = append(routed, d) }
			agency := NewAgency(p, jobs, tc.worker, &config.Config{WorkDir: dir}, nil)
			agency.Wire()

			d := tc.prepare(t, agency, dir)
			metrics := newTurnMetrics()
			turn := p.newTurnConductor(metrics)
			agency.HandleRoute(context.Background(), turn, metrics, d)
			if agency.narrator != nil && agency.narrator.IsActive() {
				agency.narrator.handleStop()
			}

			if len(routed) != 1 {
				t.Fatalf("OnRoute calls = %d, want 1: %+v", len(routed), routed)
			}
			if routed[0] != d {
				t.Fatalf("OnRoute = %+v, want %+v", routed[0], d)
			}
		})
	}
}

// completeRoutedTurn without an Agency still notifies directly, so the unit
// tests that drive a bare Pipeline keep seeing decisions.
func TestCompleteRoutedTurnWithoutAgencyNotifiesOnce(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	var routed []RouteDecision
	p.OnRoute = func(d RouteDecision) { routed = append(routed, d) }
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	p.completeRoutedTurn(context.Background(), turn, metrics, "search the web for rust", RouteSpawn, "intent")
	if len(routed) != 1 {
		t.Fatalf("OnRoute calls = %d, want 1", len(routed))
	}
}
