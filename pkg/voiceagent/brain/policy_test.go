package brain

import (
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func policyCfg() *config.Config {
	return &config.Config{AgentName: "Samantha", EnvironmentContextEnabled: true, VoiceToolsEnabled: true}
}

// The defect this whole sequence exists to fix: the same persona behaved
// differently depending on which brain was selected, because ollama received
// environment grounding but no turn instruction while Claude and Grok received
// the reverse.
//
// Elements that define personality and grounding must be identical across
// providers. Elements that adapt to a provider's native capabilities must not
// be — and every such exception states its reason.
func TestSystemPromptPolicyIsIdenticalAcrossProviders(t *testing.T) {
	cfg := policyCfg()

	// Everything except the documented provider-adaptive element.
	shared := []PromptElement{ElemPersona, ElemApplication, ElemEnvironment, ElemTurn}

	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok} {
		got := SystemPromptPolicy(provider, cfg)

		var universal []PromptElement
		for _, e := range got {
			if e != ElemSkills && e != ElemTools {
				universal = append(universal, e)
			}
		}
		if !slices.Equal(universal, shared) {
			t.Errorf("provider %q: universal elements = %v, want %v\n"+
				"Personality and grounding elements must match across providers; "+
				"only provider-adaptive elements may differ.", provider, universal, shared)
		}
	}
}

// The one deliberate asymmetry. If this test fails because someone extended the
// skills menu to Claude or Grok, read the failure message before "fixing" it:
// that change would duplicate a surface those providers already have.
func TestSkillsMenuIsDeliberatelyOllamaOnly(t *testing.T) {
	cfg := policyCfg()

	if !slices.Contains(SystemPromptPolicy(providerOllama, cfg), ElemSkills) {
		t.Error("ollama must receive the skills menu — it has no native skill routing, " +
			"so the menu is how skills reach it at all")
	}

	for _, provider := range []string{providerClaude, providerCursor, providerGrok} {
		if slices.Contains(SystemPromptPolicy(provider, cfg), ElemSkills) {
			t.Errorf("provider %q must NOT receive samantha's skills menu.\n"+
				"This asymmetry is deliberate (not an oversight): %s runs its own tool "+
				"and skill routing, so injecting our progressive-disclosure catalog would "+
				"duplicate that surface, compete with its native tool selection, and spend "+
				"system-prompt budget on instructions it has a better mechanism for.",
				provider, provider)
		}
	}
}

func TestToolSurfaceIsDeliberatelyOllamaOnly(t *testing.T) {
	cfg := policyCfg()

	if !slices.Contains(SystemPromptPolicy(providerOllama, cfg), ElemTools) {
		t.Error("ollama must receive the tool surface — route_only vs worker tools is how it knows what it can call")
	}

	for _, provider := range []string{providerClaude, providerCursor, providerGrok} {
		got := AssembleSystemPrompt(SystemPromptInput{
			Provider: provider,
			Persona:  "PERSONA",
			WorkDir:  "/work",
			Cfg:      cfg,
		})
		if slices.Contains(SystemPromptPolicy(provider, cfg), ElemTools) {
			t.Errorf("provider %q must NOT receive Obey Voice's tool list", provider)
		}
		if strings.Contains(got, "web_search") || strings.Contains(got, "Live tool: route") {
			t.Errorf("provider %q leaked ollama tool surface:\n%s", provider, got)
		}
	}
}

// TestSkillMenuAppliesExportedForCLI guards the exported rename
// (skillMenuApplies -> SkillMenuApplies): `skills list --json` uses this
// directly to report provider_supported rather than duplicating the
// ollama-only rule.
func TestSkillMenuAppliesExportedForCLI(t *testing.T) {
	if !SkillMenuApplies(providerOllama) {
		t.Error("SkillMenuApplies(ollama) = false, want true")
	}
	for _, provider := range []string{providerClaude, providerCursor, providerGrok, "unknown"} {
		if SkillMenuApplies(provider) {
			t.Errorf("SkillMenuApplies(%q) = true, want false", provider)
		}
	}
}

// Error case: environment grounding is opt-out, and opting out must remove it
// everywhere rather than only where it happened to be wired.
func TestEnvironmentContextCanBeDisabledUniformly(t *testing.T) {
	cfg := policyCfg()
	cfg.EnvironmentContextEnabled = false

	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok} {
		if slices.Contains(SystemPromptPolicy(provider, cfg), ElemEnvironment) {
			t.Errorf("provider %q still includes environment grounding when disabled", provider)
		}
	}
}

// A nil config must behave as the default rather than silently dropping
// grounding — a benchmark or test harness that passes nil should see production
// behaviour.
func TestNilConfigKeepsEnvironmentGrounding(t *testing.T) {
	if !slices.Contains(SystemPromptPolicy(providerOllama, nil), ElemEnvironment) {
		t.Error("nil config must default to environment grounding enabled")
	}
}

// Assembly must follow the policy's order, not just its membership: the persona
// has to lead, or the model reads grounding before it knows who it is.
func TestAssembledPromptFollowsPolicyOrder(t *testing.T) {
	cfg := policyCfg()
	catalog := []skills.Skill{{Name: "hello", Description: "greeting"}}

	got := AssembleSystemPrompt(SystemPromptInput{
		Provider: providerOllama,
		Persona:  "PERSONA_MARKER",
		WorkDir:  "/work",
		Skills:   catalog,
		Turn:     "TURN_MARKER",
		Cfg:      cfg,
	})

	personaAt := strings.Index(got, "PERSONA_MARKER")
	envAt := strings.Index(got, "Environment:")
	toolsAt := strings.Index(got, "Live tool: route")
	skillsAt := strings.Index(got, "Available skills")
	turnAt := strings.Index(got, "TURN_MARKER")

	for _, c := range []struct {
		name string
		idx  int
	}{{"persona", personaAt}, {"environment", envAt}, {"tools", toolsAt}, {"skills", skillsAt}, {"turn", turnAt}} {
		if c.idx < 0 {
			t.Fatalf("assembled prompt is missing the %s element:\n%s", c.name, got)
		}
	}
	if !(personaAt < envAt && envAt < toolsAt && toolsAt < skillsAt && skillsAt < turnAt) {
		t.Errorf("elements out of policy order: persona=%d env=%d tools=%d skills=%d turn=%d",
			personaAt, envAt, toolsAt, skillsAt, turnAt)
	}
	if strings.Contains(got, "You have tools available: list_files") {
		t.Fatal("route_only ollama prompt must not advertise worker tools")
	}
}

// Claude and Grok append the turn instruction to the user message instead, so
// it must not also appear in their system prompt — duplicating it would spend
// budget twice and give the instruction two different positions of emphasis.
func TestTurnInstructionPlacementIsProviderSpecific(t *testing.T) {
	cfg := policyCfg()

	for _, provider := range []string{providerClaude, providerCursor, providerGrok} {
		got := AssembleSystemPrompt(SystemPromptInput{
			Provider: provider,
			Persona:  "PERSONA_MARKER",
			WorkDir:  "/work",
			Turn:     "TURN_MARKER",
			Cfg:      cfg,
		})
		if strings.Contains(got, "TURN_MARKER") {
			t.Errorf("provider %q put the turn instruction in the system prompt; "+
				"it is appended to the user message on this path", provider)
		}
		if TurnGoesInSystemPrompt(provider) {
			t.Errorf("provider %q should not place the turn instruction in the system prompt", provider)
		}
	}

	if !TurnGoesInSystemPrompt(providerOllama) {
		t.Error("ollama rebuilds its system prompt per turn, so the instruction rides there")
	}
}

func TestToolSurfaceContextMatchesRouterPolicy(t *testing.T) {
	live := ToolSurfaceContext(&config.Config{VoiceToolsEnabled: true})
	if !strings.Contains(live, "Live tool: route") {
		t.Fatalf("route_only surface missing route:\n%s", live)
	}
	if strings.Contains(live, "You have tools available: list_files") {
		t.Fatalf("route_only surface advertised worker tools:\n%s", live)
	}

	legacy := ToolSurfaceContext(&config.Config{VoiceToolsEnabled: true, VoiceRouterTools: string(RouterToolsLegacy)})
	if !strings.Contains(legacy, "web_search") {
		t.Fatalf("legacy surface missing web_search:\n%s", legacy)
	}

	if got := ToolSurfaceContext(&config.Config{VoiceToolsEnabled: false}); got != "" {
		t.Fatalf("tools-disabled surface = %q, want empty", got)
	}
}

func TestAssembleSystemPromptUsesDisplayName(t *testing.T) {
	cfg := policyCfg()
	cfg.UserDisplayName = "Lance"
	cfg.RuntimeEnvUser = "lancerogers"
	got := AssembleSystemPrompt(SystemPromptInput{
		Provider: providerClaude,
		Persona:  "PERSONA",
		WorkDir:  "/work",
		Cfg:      cfg,
	})
	if !strings.Contains(got, "- User: Lance") {
		t.Fatalf("environment user missing display name:\n%s", got)
	}
	if strings.Contains(got, "lancerogers") {
		t.Fatalf("OS login leaked into environment:\n%s", got)
	}
}

// A worker's boundary is grounding, so it is identical across providers and
// absent from the live brain, which has no boundary to keep.
func TestBoundaryElementIsForFencedWorkersOnly(t *testing.T) {
	live := policyCfg()
	worker := policyCfg()
	worker.WorkerBoundary = "/camp/root"
	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok} {
		if slices.Contains(SystemPromptPolicy(provider, live), ElemBoundary) {
			t.Errorf("provider %q: live brain carries a boundary", provider)
		}
		got := SystemPromptPolicy(provider, worker)
		env, boundary := slices.Index(got, ElemEnvironment), slices.Index(got, ElemBoundary)
		if boundary < 0 || boundary != env+1 {
			t.Errorf("provider %q: elements = %v, want boundary right after environment", provider, got)
		}
	}
	prompt := AssembleSystemPrompt(SystemPromptInput{Provider: providerClaude, Persona: "You are Sam.", WorkDir: "/camp/root/sub", Cfg: worker})
	if !strings.Contains(prompt, "Boundary:") || !strings.Contains(prompt, "stays under /camp/root") {
		t.Fatalf("assembled worker prompt lacks the boundary:\n%s", prompt)
	}
	if strings.Contains(AssembleSystemPrompt(SystemPromptInput{Provider: providerClaude, Persona: "x", Cfg: live}), "Boundary:") {
		t.Fatal("live prompt carries a boundary")
	}
}

func TestApplicationGroundingExplainsCurrentConversationAcrossProviders(t *testing.T) {
	for _, provider := range []string{"ollama", "claude", "cursor", "grok", "codex"} {
		prompt := AssembleSystemPrompt(SystemPromptInput{Provider: provider, Persona: "You are Venessa.", Cfg: &config.Config{}})
		for _, want := range []string{"user's current conversation", "multiple distinct agent personas", "ongoing conversation, not a new app or setup task"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s lacks %q: %s", provider, want, prompt)
			}
		}
	}
}
