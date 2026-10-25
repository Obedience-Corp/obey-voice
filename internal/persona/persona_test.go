package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	config.SetConfigDirForTest(t, dir)
}

func TestValidateID(t *testing.T) {
	for _, id := range []string{"samantha", "festival", "obey", "my-agent", "a1"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "Samantha", "has space", "under_score", "-lead", "trail-", "a--b"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestWriteLoadListApply(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	p := &Profile{
		Schema:      Schema,
		ID:          "festival",
		DisplayName: "Festival",
		Builtin:     true,
		TTS:         TTS{Provider: "kokoro", Voice: "af_bella"},
		Prompts:     PromptRefs{Persona: "festival", Turn: "festival"},
	}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}

	got, err := Load("festival")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Festival" || got.TTS.Voice != "af_bella" || got.TTS.Provider != "kokoro" {
		t.Fatalf("Load() = %+v", got)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "festival" {
		t.Fatalf("List() = %+v", list)
	}

	cfg := &config.Config{
		AgentName:     "Old",
		Persona:       "samantha",
		TTSProvider:   "kokoro",
		TTSVoice:      "af_heart",
		ActivePersona: "festival",
	}
	Apply(cfg, got)
	if cfg.AgentName != "Festival" || cfg.Persona != "festival" || cfg.TTSVoice != "af_bella" || cfg.TTSProvider != "kokoro" {
		t.Fatalf("Apply overlay failed: %+v", cfg)
	}
}

func TestListSkipsMalformedProfileAndReportsIssue(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	valid := &Profile{
		Schema: Schema, ID: "healthy", DisplayName: "Healthy",
		Prompts: PromptRefs{Persona: "healthy"},
	}
	if err := Write(valid, false); err != nil {
		t.Fatal(err)
	}
	brokenPath := filepath.Join(dir, "personas", "broken", "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(brokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenPath, []byte("schema: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	profiles, err := List()
	if err != nil || len(profiles) != 1 || profiles[0].ID != "healthy" {
		t.Fatalf("List() = %v, %v; want healthy profile only", profiles, err)
	}
	profiles, issues, err := ListWithIssues()
	if err != nil || len(profiles) != 1 || len(issues) != 1 || issues[0].ID != "broken" {
		t.Fatalf("ListWithIssues() = %v, %+v, %v", profiles, issues, err)
	}
}

func TestApplyQwenProviderAndVoice(t *testing.T) {
	cfg := &config.Config{
		TTSProvider:  "kokoro",
		TTSVoice:     "af_heart",
		QwenTTSVoice: "",
	}
	Apply(cfg, &Profile{
		ID:          "obey",
		DisplayName: "Obey",
		Prompts:     PromptRefs{Persona: "obey"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Vivian"},
	})
	if cfg.TTSProvider != "qwen3-tts" {
		t.Fatalf("TTSProvider = %q, want qwen3-tts", cfg.TTSProvider)
	}
	if cfg.QwenTTSVoice != "Vivian" {
		t.Fatalf("QwenTTSVoice = %q, want Vivian", cfg.QwenTTSVoice)
	}
	// Kokoro voice left as-is (not cleared) — factory uses provider to pick keys.
	if cfg.TTSVoice != "af_heart" {
		t.Fatalf("TTSVoice = %q, want af_heart unchanged", cfg.TTSVoice)
	}
}

func TestApplyQwenTier(t *testing.T) {
	// A qwen persona pins its own native model tier; spelling variants
	// normalize to the canonical tier name.
	cfg := &config.Config{TTSProvider: "kokoro", QwenTTSModelTier: "0.6b"}
	Apply(cfg, &Profile{
		ID:          "veronica",
		DisplayName: "Veronica",
		Prompts:     PromptRefs{Persona: "veronica"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Ono_Anna", Tier: "1.7"},
	})
	if cfg.QwenTTSModelTier != "1.7b" {
		t.Fatalf("QwenTTSModelTier = %q, want 1.7b", cfg.QwenTTSModelTier)
	}

	// Empty tier inherits the app-level setting.
	cfg = &config.Config{QwenTTSModelTier: "1.7b"}
	Apply(cfg, &Profile{
		ID:          "dylan",
		DisplayName: "Dylan",
		Prompts:     PromptRefs{Persona: "dylan"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Vivian"},
	})
	if cfg.QwenTTSModelTier != "1.7b" {
		t.Fatalf("QwenTTSModelTier = %q, want inherited 1.7b", cfg.QwenTTSModelTier)
	}

	// A non-qwen persona never touches the tier, even if one is recorded.
	cfg = &config.Config{QwenTTSModelTier: "0.6b"}
	Apply(cfg, &Profile{
		ID:          "samantha",
		DisplayName: "Samantha",
		Prompts:     PromptRefs{Persona: "samantha"},
		TTS:         TTS{Provider: "kokoro", Voice: "af_sky", Tier: "1.7b"},
	})
	if cfg.QwenTTSModelTier != "0.6b" {
		t.Fatalf("QwenTTSModelTier = %q, want untouched 0.6b", cfg.QwenTTSModelTier)
	}
}

func TestValidateRejectsUnknownTier(t *testing.T) {
	p := &Profile{
		Schema: Schema, ID: "x", DisplayName: "X",
		Prompts: PromptRefs{Persona: "x"},
		TTS:     TTS{Provider: "qwen3-tts", Tier: "3b"},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate() = nil, want unknown-tier error")
	}
	p.TTS.Tier = "1.7b"
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for known tier", err)
	}
}

func TestFromConfigCapturesQwenVoice(t *testing.T) {
	p := FromConfig(&config.Config{
		AgentName:    "Q",
		Persona:      "samantha",
		TTSProvider:  "qwen3-tts",
		QwenTTSVoice: "Ryan",
		TTSVoice:     "af_heart",
	})
	if p.TTS.Provider != "qwen3-tts" || p.TTS.Voice != "Ryan" {
		t.Fatalf("FromConfig TTS = %+v", p.TTS)
	}
}

func TestUpdateStackPersistsBrainAndTTS(t *testing.T) {
	// UpdateStack replaces UpdateActiveTTS as the only write path for a
	// persona's model/voice stack (WI-c8884d §5.2): it targets one profile
	// by id and never touches live config or global keys.
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := Write(&Profile{
		Schema: Schema, ID: "samantha", DisplayName: "Samantha",
		TTS:     TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	p, err := UpdateStack("samantha",
		Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS{Provider: "qwen3-tts", Voice: "Ryan", Tier: "1.7"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.Provider != "ollama" || p.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("returned brain = %+v", p.Brain)
	}

	profile, err := Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Brain.Provider != "ollama" || profile.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("persisted brain = %+v, want ollama/qwen2.5:14b", profile.Brain)
	}
	if profile.TTS.Provider != "qwen3-tts" || profile.TTS.Voice != "Ryan" || profile.TTS.Tier != "1.7b" {
		t.Fatalf("persisted TTS = %+v, want qwen3-tts/Ryan/1.7b", profile.TTS)
	}

	// Empty fields clear back to inherit-global.
	if _, err := UpdateStack("samantha", Brain{}, TTS{}); err != nil {
		t.Fatal(err)
	}
	profile, err = Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Brain.Provider != "" || profile.TTS.Provider != "" {
		t.Fatalf("cleared stack = brain %+v tts %+v, want inherit-global", profile.Brain, profile.TTS)
	}
}

func TestApplyRoutesPersonaBrainModel(t *testing.T) {
	cfg := &config.Config{BrainProvider: "claude", OllamaModel: "llama3"}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "research", DisplayName: "Research",
		Brain:   Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		Prompts: PromptRefs{Persona: "research"},
	})
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "qwen2.5:14b" {
		t.Fatalf("cfg = provider %q model %q, want ollama/qwen2.5:14b", cfg.BrainProvider, cfg.OllamaModel)
	}

	// Empty brain fields inherit the app defaults untouched.
	cfg = &config.Config{BrainProvider: "ollama", OllamaModel: "llama3"}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "plain", DisplayName: "Plain",
		Prompts: PromptRefs{Persona: "plain"},
	})
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "llama3" {
		t.Fatalf("cfg mutated by empty brain: provider %q model %q", cfg.BrainProvider, cfg.OllamaModel)
	}
}

// A config file with no personas dir is a first run: seed the catalog, default
// to obey. There is no Samantha-shaped migration of leftover single-agent keys.
func TestEnsureAndApplySeedsCatalogWhenPersonasDirIsEmpty(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	writeConfigFile(t, dir, "agent_name: Custom\ntts_voice: af_nova\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, DefaultID)
	}
	for _, id := range ShippedIDs() {
		if !Exists(id) {
			t.Errorf("%s was not seeded", id)
		}
	}

	path := ProfilePath("samantha")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(raw2) {
		t.Fatal("the second load rewrote a shipped profile")
	}
}

func TestEnsureAndApplyMissingActive(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	// Seed one profile, then request a missing id.
	if err := Write(&Profile{
		Schema:      Schema,
		ID:          "samantha",
		DisplayName: "Samantha",
		Builtin:     true,
		Prompts:     PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	// Declared in the file, so it is a choice the user made and gets an error
	// rather than a silent heal onto some other persona.
	writeConfigFile(t, dir, "active_persona: nope\n")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing active_persona")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileIDMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "samantha", "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`schema: obey-voice.persona.v1
id: festival
display_name: X
prompts:
  persona: festival
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "does not match directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestFromConfig(t *testing.T) {
	p := FromConfig(&config.Config{
		AgentName: "A",
		Persona:   "samantha",
		TTSVoice:  "v",
	})
	if p.ID != "samantha" || p.DisplayName != "A" || p.TTS.Voice != "v" {
		t.Fatalf("%+v", p)
	}
}
