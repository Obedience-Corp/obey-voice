package brain

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestApplicationCapabilitiesReachProvidersWithoutSkills(t *testing.T) {
	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok, "codex"} {
		for _, enabled := range []bool{false, true} {
			cfg := &config.Config{CalibreEnabled: enabled}
			prompt := AssembleSystemPrompt(SystemPromptInput{Provider: provider, Persona: "You are Venessa.", Cfg: cfg})
			for _, capability := range []string{"audiobook create", "--from-library", "obey-voice render", "persona list/show/create/edit", "config get/schema/set"} {
				if !strings.Contains(prompt, capability) {
					t.Errorf("provider=%s skills off: missing %q", provider, capability)
				}
			}
			want := "Current Calibre setting: calibre_enabled=false"
			if enabled {
				want = "Current Calibre setting: calibre_enabled=true"
			}
			if !strings.Contains(prompt, want) {
				t.Errorf("provider=%s calibre=%v not reflected in prompt", provider, enabled)
			}
		}
	}
}
