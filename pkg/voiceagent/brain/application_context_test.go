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
			cfg.PromptsDir = t.TempDir()
			prompt := ApplicationRequestContext(cfg, provider, "Can Obey Voice convert a Calibre ebook to audio?")
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

func TestApplicationContextIsRequestSpecificAcrossProviders(t *testing.T) {
	cfg := &config.Config{AgentName: "Venessa", PromptsDir: t.TempDir(), SkillsEnabled: true}
	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok, "codex"} {
		t.Run(provider, func(t *testing.T) {
			persistent := AssembleSystemPrompt(SystemPromptInput{Provider: provider, Persona: "You are Venessa.", Cfg: cfg})
			if strings.Contains(persistent, "audiobook create") || strings.Contains(persistent, "calibre_enabled") {
				t.Fatal("application instructions leaked into persistent system prompt")
			}
			history := []Turn{{Role: "user", Content: "Can Obey Voice create an audiobook?"}, {Role: "assistant", Content: "Yes."}}
			project := func(input string, resumed bool) string {
				turns := append(append([]Turn(nil), history...), Turn{Role: "user", Content: input})
				session := ""
				if resumed {
					session = "existing-session"
				}
				switch provider {
				case providerClaude:
					b := &Brain{cfg: cfg, history: turns, sessionID: session}
					return b.buildPrompt(StreamOptions{OmitTurnInstruction: true})
				case providerCursor:
					b := &CursorBrain{cfg: cfg, history: turns, sessionID: session}
					return b.buildPrompt(StreamOptions{OmitTurnInstruction: true})
				case providerGrok:
					b := &GrokBrain{cfg: cfg, history: turns, sessionID: session}
					return b.buildPrompt(StreamOptions{OmitTurnInstruction: true})
				case "codex":
					b := &CodexBrain{cfg: cfg, canonical: turns, sessionID: session}
					return b.buildPrompt(input, StreamOptions{})
				default:
					b := &OllamaBrain{cfg: cfg}
					b.LoadHistory(turns)
					return b.buildMessages("")[0].Content
				}
			}
			for _, resumed := range []bool{false, true} {
				for _, input := range []string{
					"How do I configure Obey Voice?",
					"Can you convert a Calibre ebook to audio?",
					"Convert this Markdown file to audio",
					"Render this HTML document as speech",
					"Read this plain text aloud",
					"Turn this URL into an audiobook",
					"Read https://example.com/article aloud",
					"Convert notes.md to audio",
					"Render notes.txt as speech",
					"Read article.html aloud",
					"Read article.htm aloud",
				} {
					if got := project(input, resumed); !strings.Contains(got, "Current Calibre setting: calibre_enabled=false") {
						t.Fatalf("resumed=%v relevant input missing app context: %q", resumed, got)
					}
				}
				for _, input := range []string{
					"Tell me a joke",
					"Help me configure PostgreSQL",
					"What audiobook did you enjoy?",
					"Write a poem about Obey Voice",
					"yes",
					"Build a PDF to audio service in my project",
					"Write a Python script to convert an EPUB to audio",
					"Implement Markdown to audio conversion in my project",
					"Build an HTML to speech service",
					"Write code to read text aloud",
					"Create a URL to audiobook API",
					"Write a script to read https://example.com/article aloud",
					"Implement a notes.md to audio converter",
				} {
					if got := project(input, resumed); strings.Contains(got, "calibre_enabled") || strings.Contains(got, "obey-voice audiobook create") {
						t.Fatalf("resumed=%v unrelated input %q includes app context: %s", resumed, input, got)
					}
				}
			}
		})
	}
}
