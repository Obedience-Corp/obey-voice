//go:build integration

package brain

import (
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Explicit opt-in uses the installed harness with a disposable working directory
// and tools-off private requests. No microphone, personal persona, or live session.
func TestLiveHarnessRouting(t *testing.T) {
	provider := os.Getenv("OBEY_VOICE_LIVE_HARNESS_PROVIDER")
	if provider == "" {
		t.Skip("set OBEY_VOICE_LIVE_HARNESS_PROVIDER to an installed authenticated harness")
	}
	model := os.Getenv("OBEY_VOICE_LIVE_HARNESS_MODEL")
	cfg := &config.Config{BrainProvider: provider, WorkDir: t.TempDir(), PromptsDir: t.TempDir(), Persona: "obey",
		MaxHistory: 20, AgentName: "Obey", CodexModel: model, CodexReasoningEffort: "low", CursorModel: model,
		GrokModel: model, MuseSparkModel: model, ObeyModel: model, ObeyToolPolicy: "daemon", PersonaRoutingTimeout: 60}
	b, err := NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	router, ok := b.(LiveRouteClassifier)
	if !ok {
		t.Fatalf("provider %q has no harness route classifier", provider)
	}
	history := []Turn{{Role: "user", Content: "My location is Denver, Colorado."}}
	for _, tc := range []struct{ text, route string }{
		{"Can you tell me what the weather is today?", "spawn"},
		{"What about tomorrow?", "spawn"},
		{"How are you doing today?", ""},
		{"Explain why rain forms.", ""},
		{"Can this app search the web?", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			context := history
			if tc.text == "What about tomorrow?" {
				context = append(append([]Turn(nil), history...), Turn{Role: "user", Content: "Please check today's weather."}, Turn{Role: "assistant", Content: "The forecast was retrieved."})
			}
			r, err := router.ClassifyLiveRoute(t.Context(), LiveRouteRequest{Text: tc.text, Conversation: context, WorkerTiers: &WorkerTierAvailability{Simple: true}})
			t.Logf("provider=%s model=%s route=%q tier=%q err=%v", provider, model, r.Route, r.RouteTier, err)
			if err != nil || r.Route != tc.route {
				t.Fatalf("unexpected route: %+v err=%v", r, err)
			}
			if r.Route == "spawn" && r.RouteTier != "simple" {
				t.Fatalf("wrong effort: %+v", r)
			}
			if len(b.History()) != 0 {
				t.Fatal("private routing polluted conversation")
			}
		})
	}
}
