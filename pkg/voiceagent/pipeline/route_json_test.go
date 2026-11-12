package pipeline

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type optionsRecordingBrain struct {
	*fakeBrain
	opts []brain.StreamOptions
}

func (b *optionsRecordingBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.opts = append(b.opts, opts)
	return b.fakeBrain.ThinkStream(ctx, input, opts)
}

// json_route rides on the route_only policy; it never applies to the legacy
// tool list or to a turn without tools.
func TestLiveTurnsCarryJSONRouteOnlyWithRouteOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tools  bool
		policy brain.RouterToolsPolicy
		json   bool
		want   bool
	}{
		{"json_route", true, brain.RouterToolsRouteOnly, true, true},
		{"route_only", true, brain.RouterToolsRouteOnly, false, false},
		{"legacy", true, brain.RouterToolsLegacy, true, false},
		{"tools off", false, brain.RouterToolsRouteOnly, true, false},
	} {
		for _, voice := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/text", true: "/voice"}[voice], func(t *testing.T) {
				b := &optionsRecordingBrain{fakeBrain: &fakeBrain{chunks: []string{"Hello."}}}
				p := &Pipeline{Brain: b, Events: events.NewBus(), VoiceToolsEnabled: tc.tools, RouterToolsPolicy: tc.policy, RouteJSON: tc.json}
				p.SetOutputMuted(true)
				var err error
				if voice {
					p.STT = &fakeSTT{text: "How are you today?"}
					_, err = p.RunTurn(t.Context())
				} else {
					err = p.RunTurnTextMode(t.Context(), "How are you today?")
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(b.opts) != 1 || b.opts[0].RouteJSON != tc.want {
					t.Fatalf("opts=%+v want RouteJSON=%v", b.opts, tc.want)
				}
			})
		}
	}
}
