package persona

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"testing"
)

func TestObeyPersonaModelRouting(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", ObeyProvider: "configured", ObeyCamp: "camp"}
	applyBrain(cfg, Brain{Provider: "obey", Model: "persona-model"})
	if cfg.BrainProvider != "obey" || ModelForProvider(cfg, "obey") != "persona-model" || cfg.ObeyProvider != "configured" || cfg.ObeyCamp != "camp" {
		t.Fatal("daemon routing lost")
	}
}
