package config

import (
	"strings"
	"testing"
)

func TestDecisionSetupInspectsDisabledSelectionWithoutMutatingConfig(t *testing.T) {
	cfg := &Config{ModelsDir: t.TempDir(), DecisionLocalModel: "d1-3b", DecisionProvider: "openai"}
	s, err := DecisionSetupStatus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DecisionEnabled || cfg.DecisionProvider != "openai" {
		t.Fatal("setup mutated caller")
	}
	for _, want := range []string{"Liquid", cfg.ModelsDir, "Needs install or repair", "LFM"} {
		if !strings.Contains(s, want) {
			t.Fatal(s)
		}
	}
}
