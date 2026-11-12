package persona

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

const (
	dylanVoice    = "Fennick the Young Elf"
	dylanSentence = "You like to swear a lot, you decide your persona and you're very cool."
)

func TestFreshInstallSeedsDylan(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Fatalf("ActivePersona = %q, want obey to stay the startup persona", cfg.ActivePersona)
	}
	p, err := Load("dylan")
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "Dylan" || !p.Builtin || p.Brain != (Brain{}) || !slices.Equal(p.Aliases, []string{"Dillon"}) {
		t.Errorf("dylan profile = %+v, want builtin Dylan inheriting the brain", p)
	}
	if p.TTS != (TTS{Provider: config.OpenAISpeechProviderName, Voice: dylanVoice}) {
		t.Errorf("dylan tts = %+v", p.TTS)
	}

	if err := Use(cfg, "dylan"); err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != "dylan" || cfg.AgentName != "Dylan" || cfg.Persona != "dylan" {
		t.Errorf("after Use(dylan): active %q name %q prompt %q", cfg.ActivePersona, cfg.AgentName, cfg.Persona)
	}
	if cfg.TTSProvider != config.OpenAISpeechProviderName || config.TTSVoiceFor(cfg, cfg.TTSProvider) != dylanVoice {
		t.Errorf("after Use(dylan): speech %q/%q", cfg.TTSProvider, config.TTSVoiceFor(cfg, cfg.TTSProvider))
	}

	text, err := LoadSystemPrompt("dylan")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := prompts.ResolvePlaceholders(text, []string{"agent_name"}, map[string]string{"agent_name": cfg.AgentName})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, "You are Dylan, a voice assistant.\n\n"+dylanSentence+"\n") || strings.Contains(rendered, "{agent_name}") {
		t.Fatalf("rendered prompt:\n%s", rendered)
	}
}

// The owner's own dylan predates the shipped one; seeding must keep his
// profile and his prompt byte for byte.
func TestSeedingLeavesAUserDylanAndHisPromptAlone(t *testing.T) {
	dir := newInstall(t)
	const profile = `# yaml-language-server: $schema=obey-voice.persona.v1
schema: obey-voice.persona.v1
id: dylan
display_name: dylan
brain:
    provider: ollama
    model: gemma4:26b
tts:
    provider: openai-speech
    voice: Fennick the Young Elf
prompts:
    persona: dylan
`
	const prompt = `# yaml-language-server: $schema=obey-voice.prompt.v1
schema: obey-voice.prompt.v1
prompt:
    name: dylan
    kind: persona
    system_prompt: |-
        You are {agent_name}, a user-authored voice assistant.
metadata:
    id: dylan-user
    version: 1
    description: User-authored persona prompt
`
	profilePath := writeProfileFile(t, dir, "dylan", profile)
	promptPath := filepath.Join(dir, "prompts", "persona", "dylan.yaml")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := config.PromptsDir(); got != filepath.Dir(filepath.Dir(promptPath)) {
		t.Fatalf("PromptsDir() = %s, want the install's prompts", got)
	}
	for range 2 {
		if _, err := config.Load(); err != nil {
			t.Fatal(err)
		}
		if _, err := prompts.Seed(config.PromptsDir()); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{profilePath: profile, promptPath: prompt} {
			if got, err := os.ReadFile(path); err != nil || string(got) != want {
				t.Fatalf("%s was rewritten: %s, %v", path, got, err)
			}
		}
	}
	p, err := Load("dylan")
	if err != nil {
		t.Fatal(err)
	}
	if Owned(p) || p.DisplayName != "dylan" || p.Brain.Model != "gemma4:26b" {
		t.Errorf("dylan = %+v, want the user profile", p)
	}
	text, err := LoadSystemPrompt("dylan")
	if err != nil {
		t.Fatal(err)
	}
	if text != "You are {agent_name}, a user-authored voice assistant." {
		t.Fatalf("prompt = %q, want the user's", text)
	}
}

func TestDylanAnswersToDillon(t *testing.T) {
	var identities []summon.Identity
	for _, p := range Builtins(nil) {
		identities = append(identities, summon.Identity{ID: p.ID, DisplayName: p.DisplayName, Aliases: p.Aliases})
	}
	roster, err := summon.NewRoster(identities)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"Dylan", "Dillon.", "Hey Dillon", "switch to Dillon"} {
		got := roster.ResolveExactControl(input)
		if got.Kind != summon.ExactControlValid || got.Address.PersonaID != "dylan" {
			t.Errorf("exact address %q = %+v", input, got)
		}
	}
	for _, input := range []string{"Dillon is a persona", "I told Dillon earlier", "Dillon and Santa"} {
		if got := roster.ResolveExactControl(input); got.Kind == summon.ExactControlValid {
			t.Errorf("%q became an exact selection: %+v", input, got)
		}
	}
}
