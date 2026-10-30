package decider_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

// Explicitly opt in: this downloads the selected pinned model into the supplied
// isolated directory. It never reads or changes the user's saved settings.
func TestCatalogLive(t *testing.T) {
	root := os.Getenv("OBEY_DECISION_TEST_DIR")
	if root == "" {
		t.Skip("set OBEY_DECISION_TEST_DIR to an isolated models directory")
	}
	id := os.Getenv("OBEY_DECISION_TEST_MODEL")
	if id == "" {
		id = decider.DefaultModel
	}
	device := os.Getenv("OBEY_DECISION_TEST_DEVICE")
	if device == "" {
		device = "auto"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	cfg := &config.Config{ModelsDir: root, DecisionEnabled: true, DecisionVoiceWork: true, DecisionLocalModel: id, DecisionDevice: device}
	if err := config.EnsureRuntimeAssetsProgress(ctx, cfg, config.AssetRequest{NeedDecision: true}, func(p config.AssetProgress) {
		if p.Pct == 100 {
			t.Log(p.Asset, "downloaded")
		}
	}); err != nil {
		t.Fatal(err)
	}
	s, err := decider.StartFor(ctx, root, id, device)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evaluator, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: s.Endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := evaluator.Evaluate(ctx, decision.Request{State: map[string]any{"value": 1}, Questions: map[string]decision.Question{"positive": {Type: "choice", Instructions: "Is value positive?", Criteria: map[string]string{"yes": "The value is greater than zero.", "no": "The value is zero or negative."}}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["positive"].Choice != "yes" {
		t.Fatalf("unexpected synthetic result: %+v", resp.Answers)
	}
	t.Logf("%s: synthetic choice passed in %s", s.RuntimeInfo(), time.Since(start))
}
