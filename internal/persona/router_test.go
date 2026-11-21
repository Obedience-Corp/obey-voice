package persona

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestBrainRouterOverridesTheInstallationSetting(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "ollama", VoiceRouterTools: "direct"}
	inherit := &Profile{ID: "santa"}
	Apply(cfg, inherit)
	if cfg.VoiceRouterTools != "direct" || slices.Contains(OverriddenKeys(cfg, inherit), "voice_router_tools") {
		t.Fatalf("a persona without brain.router changed it: %q", cfg.VoiceRouterTools)
	}
	own := &Profile{ID: "obey", Brain: Brain{Router: " Single_Call "}}
	Apply(cfg, own)
	if cfg.VoiceRouterTools != "single_call" || !slices.Contains(OverriddenKeys(&config.Config{BrainProvider: "ollama", VoiceRouterTools: "direct"}, own), "voice_router_tools") {
		t.Fatalf("brain.router not applied: %q", cfg.VoiceRouterTools)
	}
}

func TestBrainRouterIsValidated(t *testing.T) {
	p := &Profile{Schema: Schema, ID: "obey", DisplayName: "Obey", Prompts: PromptRefs{Persona: "obey"}}
	for _, ok := range []string{"", "direct", "route_only", "json_route", "single_call", "legacy"} {
		p.Brain.Router = ok
		if err := p.Validate(); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	p.Brain.Router = "single-call"
	if err := p.Validate(); err == nil {
		t.Fatal("an unknown router was accepted")
	}
}

// The stack editors do not show the router, so saving the stack keeps it.
func TestUpdateStackKeepsTheRouter(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(&Profile{Schema: Schema, ID: "obey", DisplayName: "Obey", Brain: Brain{Router: "single_call"},
		TTS: TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: PromptRefs{Persona: "obey"}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStack("obey", Brain{Provider: "ollama", Model: "gemma4:26b-mlx"}, TTS{Provider: "kokoro", Voice: "af_heart"}); err != nil {
		t.Fatal(err)
	}
	p, err := Load("obey")
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.Router != "single_call" || p.Brain.Model != "gemma4:26b-mlx" {
		t.Fatalf("brain = %+v", p.Brain)
	}
}
