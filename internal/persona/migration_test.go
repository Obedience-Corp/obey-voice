package persona

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The rebrand moved the built-in default from samantha to obey. These tests
// fix what that is allowed to change — the identity a nothing-configured
// install resolves — and what it may not: everything an existing install
// already had. They go through config.Load, so they exercise the same
// afterLoad hook the CLI, the TUI and serve all enter through.

// newInstall points the config package at a throwaway install root and clears
// the environment bindings that would otherwise outrank the config file under
// a developer's exported TTS_VOICE (or a CI runner's).
func newInstall(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setConfigDir(t, dir)
	for _, name := range []string{"PERSONA", "ACTIVE_PERSONA", "TTS_PROVIDER", "TTS_VOICE", "BRAIN_PROVIDER", "OLLAMA_MODEL"} {
		t.Setenv(name, "")
	}
	return dir
}

// writeConfigFile writes config.yaml under the active install root.
func writeConfigFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeProfileFile plants a persona.yaml the way an install that already
// migrated has one, without going through Write — the point is to load bytes
// this code did not just produce.
func writeProfileFile(t *testing.T, dir, id, body string) string {
	t.Helper()
	path := filepath.Join(dir, "personas", id, "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// builtinObeyOnDisk is the obey profile as SeedBuiltins writes it: flagged
// ours, pinning nothing.
const builtinObeyOnDisk = `# yaml-language-server: $schema=obey-voice.persona.v1
schema: obey-voice.persona.v1
id: obey
display_name: Obey
builtin: true
tts: {}
prompts:
    persona: obey
`

const samanthaProfileOnDisk = `# yaml-language-server: $schema=obey-voice.persona.v1
schema: obey-voice.persona.v1
id: samantha
display_name: Samantha
builtin: true
tts:
    provider: kokoro
    voice: af_heart
prompts:
    persona: samantha
`

func loadIDs(t *testing.T) []string {
	t.Helper()
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

// TestFreshInstallResolvesObey is the one behaviour change: an install with no
// config file and no personas directory now wakes up as obey, end to end —
// active id, profile on disk, agent name, prompt document.
func TestFreshInstallResolvesObey(t *testing.T) {
	newInstall(t)

	// Capture Settings before the named Obey profile overlays its voice.
	before, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() on a fresh install: %v", err)
	}

	if cfg.ActivePersona != DefaultID {
		t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, DefaultID)
	}
	if cfg.AgentName != DefaultDisplayName {
		t.Errorf("AgentName = %q, want %q", cfg.AgentName, DefaultDisplayName)
	}
	if cfg.Persona != DefaultID {
		t.Errorf("Persona prompt ref = %q, want %q", cfg.Persona, DefaultID)
	}

	p, err := Load(DefaultID)
	if err != nil {
		t.Fatalf("Load(%s): %v", DefaultID, err)
	}
	if !p.Builtin {
		t.Error("obey profile is not marked builtin")
	}
	if p.Brain != (Brain{}) || p.TTS != (TTS{Provider: "openai-speech", Voice: "Harper"}) {
		t.Errorf("obey stack = %+v / %+v, want inherited brain and Phonon Harper", p.Brain, p.TTS)
	}
	if cfg.TTSProvider != "openai-speech" || cfg.OpenAISpeechVoice != "Harper" {
		t.Errorf("obey TTS = %s/%s, want openai-speech/Harper", cfg.TTSProvider, cfg.OpenAISpeechVoice)
	}
	if cfg.BrainProvider != before.BrainProvider || cfg.OllamaModel != before.OllamaModel {
		t.Fatal("the named default changed the Settings brain")
	}
	defaults, err := ResolveDefaultsBinding(before)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Config().TTSProvider != "openai-speech" || defaults.Config().OpenAISpeechVoice != "Harper" {
		t.Fatal("default chat must use the saved Obey speech stack")
	}

	text, err := LoadSystemPrompt(cfg.Persona)
	if err != nil {
		t.Fatalf("LoadSystemPrompt(%s): %v", cfg.Persona, err)
	}
	if !strings.Contains(text, "the voice of Obey Voice") {
		t.Errorf("default prompt is not the obey identity:\n%s", text)
	}
	if !strings.Contains(text, "{agent_name}") {
		t.Error("default prompt lost its {agent_name} placeholder")
	}
}

// TestFreshInstallStillSeedsSamantha: she is an ordinary persona now, but she
// still ships — a fresh install can select her before speech assets are installed.
func TestFreshInstallStillSeedsSamantha(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	p, err := Load("samantha")
	if err != nil {
		t.Fatalf("Load(%s): %v", "samantha", err)
	}
	if p.DisplayName != "Samantha" || !p.Builtin {
		t.Errorf("samantha profile = %+v, want display %q and builtin", p, "Samantha")
	}
	if p.TTS.Provider != "openai-speech" || p.TTS.Voice != "Juniper" {
		t.Errorf("samantha tts = %+v, want the catalog pin openai-speech/Juniper", p.TTS)
	}

	text, err := LoadSystemPrompt("samantha")
	if err != nil {
		t.Fatalf("LoadSystemPrompt(samantha): %v", err)
	}
	if !strings.Contains(text, "inspired by the character from the movie Her") {
		t.Errorf("samantha prompt is not her identity:\n%s", text)
	}

	// Switching to her carries her identity onto the config.
	if err := Use(cfg, "samantha"); err != nil {
		t.Fatalf("Use(samantha): %v", err)
	}
	if cfg.ActivePersona != "samantha" || cfg.AgentName != "Samantha" || cfg.Persona != "samantha" {
		t.Errorf("after Use(samantha): active %q name %q prompt %q", cfg.ActivePersona, cfg.AgentName, cfg.Persona)
	}
}

// TestFreshInstallSeedsVeronica: she is a selectable companion, not the
// default. A fresh install can switch to her without authoring a profile.
func TestFreshInstallSeedsVeronica(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Fatalf("ActivePersona = %q, want obey as the default even after Veronica ships", cfg.ActivePersona)
	}

	p, err := Load("veronica")
	if err != nil {
		t.Fatalf("Load(%s): %v", "veronica", err)
	}
	if p.DisplayName != "Veronica" || !p.Builtin {
		t.Errorf("veronica profile = %+v, want display %q and builtin", p, "Veronica")
	}
	if p.TTS.Provider != "openai-speech" || p.TTS.Voice != "Zoey" || p.TTS.Tier != "" {
		t.Errorf("veronica tts = %+v, want openai-speech Zoey without a Qwen tier", p.TTS)
	}
	if p.Brain != (Brain{}) {
		t.Errorf("veronica brain = %+v, want inherited", p.Brain)
	}

	text, err := LoadSystemPrompt("veronica")
	if err != nil {
		t.Fatalf("LoadSystemPrompt(veronica): %v", err)
	}
	if !strings.Contains(text, "inspired by the character from the movie Her") {
		t.Errorf("veronica prompt is not her public identity:\n%s", text)
	}

	if err := Use(cfg, "veronica"); err != nil {
		t.Fatalf("Use(veronica): %v", err)
	}
	if cfg.ActivePersona != "veronica" || cfg.AgentName != "Veronica" || cfg.Persona != "veronica" {
		t.Errorf("after Use(veronica): active %q name %q prompt %q", cfg.ActivePersona, cfg.AgentName, cfg.Persona)
	}
	if cfg.TTSProvider != "openai-speech" || cfg.OpenAISpeechVoice != "Zoey" {
		t.Errorf("after Use(veronica): tts %q voice %q tier %q", cfg.TTSProvider, cfg.OpenAISpeechVoice, cfg.QwenTTSModelTier)
	}
}

// TestFreshInstallSeedsJarvis: he is a selectable companion, not the default.
// A fresh install can switch to him without authoring a profile; Phonon Edwin
// is the matching voice.
func TestFreshInstallSeedsJarvis(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Fatalf("ActivePersona = %q, want obey as the default even after Jarvis ships", cfg.ActivePersona)
	}

	p, err := Load("jarvis")
	if err != nil {
		t.Fatalf("Load(%s): %v", "jarvis", err)
	}
	if p.DisplayName != "Jarvis" || !p.Builtin {
		t.Errorf("jarvis profile = %+v, want display %q and builtin", p, "Jarvis")
	}
	if p.TTS.Provider != "openai-speech" || p.TTS.Voice != "Edwin" {
		t.Errorf("jarvis tts = %+v, want openai-speech/Edwin", p.TTS)
	}
	if p.Brain != (Brain{}) {
		t.Errorf("jarvis brain = %+v, want inherited", p.Brain)
	}

	text, err := LoadSystemPrompt("jarvis")
	if err != nil {
		t.Fatalf("LoadSystemPrompt(jarvis): %v", err)
	}
	if !strings.Contains(text, "inspired by the dry British AI butler from Iron Man") {
		t.Errorf("jarvis prompt is not his public identity:\n%s", text)
	}

	if err := Use(cfg, "jarvis"); err != nil {
		t.Fatalf("Use(jarvis): %v", err)
	}
	if cfg.ActivePersona != "jarvis" || cfg.AgentName != "Jarvis" || cfg.Persona != "jarvis" {
		t.Errorf("after Use(jarvis): active %q name %q prompt %q", cfg.ActivePersona, cfg.AgentName, cfg.Persona)
	}
	if cfg.TTSProvider != "openai-speech" || cfg.OpenAISpeechVoice != "Edwin" {
		t.Errorf("after Use(jarvis): tts %q voice %q", cfg.TTSProvider, cfg.OpenAISpeechVoice)
	}
}

// TestSeedBuiltinsLeavesAUserVeronicaAlone is the privacy contract: a
// user-authored profile at id veronica is not replaced by the shipped one.
func TestSeedBuiltinsLeavesAUserVeronicaAlone(t *testing.T) {
	dir := newInstall(t)
	const private = `# yaml-language-server: $schema=obey-voice.persona.v1
schema: obey-voice.persona.v1
id: veronica
display_name: Veronica
builtin: false
tts:
    provider: kokoro
    voice: af_sky
prompts:
    persona: veronica
`
	path := writeProfileFile(t, dir, "veronica", private)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := SeedBuiltins(&config.Config{AgentName: "Obey"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("SeedBuiltins rewrote a user Veronica:\n%s", after)
	}
	p, err := Load("veronica")
	if err != nil {
		t.Fatal(err)
	}
	if p.Builtin || p.TTS.Voice != "af_sky" {
		t.Errorf("veronica = %+v, want the user profile left in place", p)
	}
}

// TestExistingSamanthaInstallKeepsSamanthaAndGainsCompanions: a config that
// names samantha stays on samantha. Missing shipped slots (obey, veronica,
// jarvis) are filled. Her own file is not rewritten.
func TestExistingSamanthaInstallKeepsSamanthaAndGainsCompanions(t *testing.T) {
	dir := newInstall(t)
	writeConfigFile(t, dir, "active_persona: samantha\npersona: samantha\nagent_name: Samantha\ntts_provider: kokoro\ntts_voice: af_heart\n")
	path := writeProfileFile(t, dir, "samantha", samanthaProfileOnDisk)
	rawBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.ActivePersona != "samantha" {
		t.Errorf("ActivePersona = %q, want samantha", cfg.ActivePersona)
	}
	if cfg.AgentName != "Samantha" {
		t.Errorf("AgentName = %q, want Samantha", cfg.AgentName)
	}
	if cfg.Persona != "samantha" {
		t.Errorf("Persona = %q, want samantha", cfg.Persona)
	}
	if cfg.TTSProvider != "kokoro" || cfg.TTSVoice != "af_heart" {
		t.Errorf("tts = %q/%q, want kokoro/af_heart", cfg.TTSProvider, cfg.TTSVoice)
	}

	text, err := LoadSystemPrompt(cfg.Persona)
	if err != nil {
		t.Fatalf("LoadSystemPrompt(%s): %v", cfg.Persona, err)
	}
	if !strings.Contains(text, "inspired by the character from the movie Her") {
		t.Errorf("samantha resolved somebody else's prompt:\n%s", text)
	}

	ids := loadIDs(t)
	if !hasProfileID(mustList(t), "samantha") || !hasProfileID(mustList(t), DefaultID) || !hasProfileID(mustList(t), "veronica") || !hasProfileID(mustList(t), "jarvis") {
		t.Errorf("personas = %v, want samantha kept and missing shipped slots filled", ids)
	}
	rawAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawBefore) != string(rawAfter) {
		t.Errorf("the profile on disk was rewritten:\nbefore:\n%s\nafter:\n%s", rawBefore, rawAfter)
	}

	// Loading twice is the upgrade path: the second run must not drift either.
	cfg2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.ActivePersona != "samantha" || cfg2.AgentName != "Samantha" || cfg2.TTSVoice != "af_heart" {
		t.Errorf("second load drifted: active %q name %q voice %q", cfg2.ActivePersona, cfg2.AgentName, cfg2.TTSVoice)
	}
}

// TestUndeclaredActivePersonaUsesTheShippedDefault: with no active_persona
// in the file, viper's default is obey. Once obey is seeded as owned, that
// is who talks. There is no second-class "heal to samantha" path.
func TestUndeclaredActivePersonaUsesTheShippedDefault(t *testing.T) {
	dir := newInstall(t)
	writeConfigFile(t, dir, "tts_provider: kokoro\n")
	writeProfileFile(t, dir, "ada", "schema: obey-voice.persona.v1\nid: ada\ndisplay_name: Ada\nprompts:\n  persona: ada\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, DefaultID)
	}
	if !hasProfileID(mustList(t), "ada") {
		t.Errorf("personas = %v, want ada kept", loadIDs(t))
	}
}

// TestConfigWithoutPersonasSeedsTheCatalog: a config file with an empty
// personas dir is a first run, not a Samantha migration. The product has not
// shipped publicly; there is no pre-personas field population to preserve.
func TestConfigWithoutPersonasSeedsTheCatalog(t *testing.T) {
	dir := newInstall(t)
	writeConfigFile(t, dir, "tts_provider: kokoro\ntts_voice: af_heart\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ActivePersona != DefaultID {
		t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, DefaultID)
	}
	for _, id := range ShippedIDs() {
		if !Exists(id) {
			t.Errorf("%s was not seeded", id)
		}
	}
}

func mustList(t *testing.T) []*Profile {
	t.Helper()
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// firstCompanionID is the fallback pickFallbackID uses after skipping an
// unowned default: ShippedIDs order, first id that is not DefaultID.
func firstCompanionID() string {
	for _, id := range ShippedIDs() {
		if id != DefaultID {
			return id
		}
	}
	return ""
}

// TestActivePersonaErrors: a persona the user chose and that is not there is an
// error, not a silent substitution — the built-in ids are the only ones that
// heal (see ensureMigrated).
func TestActivePersonaErrors(t *testing.T) {
	tests := []struct {
		name        string
		activeValue string
		wantErr     string
	}{
		{name: "invalid id", activeValue: "Not Valid", wantErr: "active_persona"},
		{name: "unknown user id", activeValue: "uncle-fu", wantErr: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newInstall(t)
			writeProfileFile(t, dir, "ada", "schema: obey-voice.persona.v1\nid: ada\ndisplay_name: Ada\nprompts:\n  persona: ada\n")
			writeConfigFile(t, dir, "active_persona: "+tt.activeValue+"\n")

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() with active_persona %q succeeded, want an error", tt.activeValue)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// Catalog pins belong to the shipped profiles, not the stack active at seed time.
func TestBuiltinsInheritBrainsAndPinVoices(t *testing.T) {
	cfg := &config.Config{AgentName: "Custom", BrainProvider: "ollama", OllamaModel: "custom", TTSProvider: "kokoro", TTSVoice: "af_sky"}
	got := Builtins(cfg)
	if len(got) == 0 || got[0].ID != DefaultID {
		t.Fatalf("Builtins() = %+v, want default first", got)
	}
	want := map[string]TTS{
		"obey":     {Provider: "openai-speech", Voice: "Harper"},
		"samantha": {Provider: "openai-speech", Voice: "Juniper"},
		"veronica": {Provider: "openai-speech", Voice: "Zoey"},
		"jarvis":   {Provider: "openai-speech", Voice: "Edwin"},
		"santa":    {Provider: "kokoro", Voice: "am_santa"},
		"dylan":    {Provider: "openai-speech", Voice: "Fennick the Young Elf"},
	}
	for _, p := range got {
		if p.Brain != (Brain{}) {
			t.Errorf("%s pins brain %+v", p.ID, p.Brain)
		}
		if voice, ok := want[p.ID]; ok {
			if p.TTS != voice {
				t.Errorf("%s TTS = %+v, want %+v", p.ID, p.TTS, voice)
			}
			delete(want, p.ID)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing shipped voices: %+v", want)
	}
}

// TestBuiltinsAreUndeletable: every catalog profile is flagged builtin.
func TestBuiltinsAreUndeletable(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ShippedIDs() {
		if _, err := Delete(cfg, id); err == nil {
			t.Errorf("Delete(%s) succeeded, want the built-in refusal", id)
		}
	}
}

// TestResolveActiveIDMatchesTheLoader: read-only surfaces resolve the same
// persona the loader heals to, so `config get` cannot report an overlay for a
// persona the agent is not running.
func TestResolveActiveIDMatchesTheLoader(t *testing.T) {
	dir := newInstall(t)
	writeConfigFile(t, dir, "tts_provider: kokoro\n")
	writeProfileFile(t, dir, "samantha", samanthaProfileOnDisk)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// A raw load never sees the heal: its active_persona is the shipped default.
	raw, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if raw.ActivePersona != DefaultID {
		t.Fatalf("raw ActivePersona = %q, want the shipped default %q", raw.ActivePersona, DefaultID)
	}
	if got := ResolveActiveID(raw); got != cfg.ActivePersona {
		t.Errorf("ResolveActiveID(raw) = %q, want the loaded %q", got, cfg.ActivePersona)
	}
}

// userProfile is a profile the USER wrote: no builtin flag, own pins. The flag
// is the provenance record every ownership decision reads, so a test that plants
// one of these is planting a slot this code may not claim.
func userProfile(id, display, provider, voice string) string {
	return "schema: obey-voice.persona.v1\nid: " + id + "\ndisplay_name: " + display +
		"\ntts:\n    provider: " + provider + "\n    voice: " + voice +
		"\nprompts:\n    persona: " + id + "\n"
}

// TestUserPersonaNamedObeyIsNotAdopted is finding 5: `obey` is not a reserved
// id, and the install this branch was written against holds user-created
// obey and obey-2..obey-6. Promoting obey to the default must not hand those
// installs a different agent, prompt, model or voice on first load.
func TestUserPersonaNamedObeyIsNotAdopted(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantActive string
	}{
		{
			// Row A2. active_persona is only the shipped default, so the obey
			// profile it names is a collision, not a choice. Fallback follows
			// ShippedIDs: default first, then alphabetical owned companions.
			name:       "defaulted active_persona skips the user's obey",
			config:     "tts_provider: kokoro\n",
			wantActive: firstCompanionID(),
		},
		{
			// The same install, one `persona use obey` later: now it IS a
			// choice, and the user's own profile is what they get.
			name:       "explicitly chosen obey is honored",
			config:     "active_persona: obey\ntts_provider: kokoro\n",
			wantActive: "obey",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newInstall(t)
			writeConfigFile(t, dir, tt.config)
			path := writeProfileFile(t, dir, "obey", userProfile("obey", "Obey", "qwen3-tts", "Ryan"))
			writeProfileFile(t, dir, "samantha", samanthaProfileOnDisk)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.ActivePersona != tt.wantActive {
				t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, tt.wantActive)
			}

			// Either way the user's file is theirs: not rewritten, not flagged
			// shipped, still deletable, pins intact.
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Errorf("the user's obey profile was rewritten:\n%s", after)
			}
			p, err := Load("obey")
			if err != nil {
				t.Fatal(err)
			}
			if p.Builtin {
				t.Error("the user's obey profile was flagged builtin — that also makes it undeletable")
			}
			if (p.TTS != TTS{Provider: "qwen3-tts", Voice: "Ryan"}) {
				t.Errorf("the user's pins moved: %+v", p.TTS)
			}
			if _, err := Delete(cfg, "obey"); err != nil && errors.Is(err, ErrBuiltinProtected) {
				t.Error("the user can no longer delete their own persona")
			}
		})
	}
}

// TestUserPersonasBesideTheDefaultAreUntouched is matrix row C: the live
// install carries obey-2..obey-6, and nothing in seeding, healing or the
// fallback order may notice them.
func TestUserPersonasBesideTheDefaultAreUntouched(t *testing.T) {
	dir := newInstall(t)
	writeConfigFile(t, dir, "active_persona: samantha\npersona: samantha\nagent_name: Samantha\ntts_provider: kokoro\ntts_voice: af_heart\n")
	writeProfileFile(t, dir, "samantha", samanthaProfileOnDisk)
	want := append([]string{}, ShippedIDs()...)
	for _, id := range []string{"obey-2", "obey-3", "obey-4", "obey-5", "obey-6"} {
		writeProfileFile(t, dir, id, userProfile(id, id, "kokoro", "af_sky"))
		want = append(want, id)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ActivePersona != "samantha" {
		t.Errorf("ActivePersona = %q, want samantha", cfg.ActivePersona)
	}
	got := loadIDs(t)
	if len(got) != len(want) {
		t.Fatalf("personas = %v, want %v", got, want)
	}
	for _, id := range want {
		if !hasProfileID(mustList(t), id) {
			t.Errorf("personas = %v, missing %q", got, id)
		}
	}
	// Each one still resolves and still belongs to the user.
	for _, id := range []string{"obey-2", "obey-6"} {
		p, err := Load(id)
		if err != nil {
			t.Fatalf("Load(%s): %v", id, err)
		}
		if p.Builtin || p.TTS.Voice != "af_sky" {
			t.Errorf("%s = %+v, want an untouched user profile", id, p)
		}
	}
}

// TestFreshDecisionIgnoresTheEnvironment: the fresh-vs-migrate decision reads
// artifacts, never keys. Both persona keys are env-bound, so a gate that read
// them could be flipped by an exported variable — which is how the first review
// found the bug by accident.
func TestFreshDecisionIgnoresTheEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantActive string
	}{
		{name: "no environment", wantActive: DefaultID},
		{name: "PERSONA exported", env: map[string]string{"PERSONA": "something"}, wantActive: DefaultID},
		{name: "ACTIVE_PERSONA at the default", env: map[string]string{"ACTIVE_PERSONA": DefaultID}, wantActive: DefaultID},
		{
			// A chosen id is still honored — it just cannot decide whether the
			// install is fresh. samantha is seeded, so this resolves.
			name:       "ACTIVE_PERSONA names the other built-in",
			env:        map[string]string{"ACTIVE_PERSONA": "samantha"},
			wantActive: "samantha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newInstall(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.ActivePersona != tt.wantActive {
				t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, tt.wantActive)
			}
			// Whichever way it went, the shipped profiles are on disk and no
			// config file was invented.
			for _, id := range ShippedIDs() {
				if !Exists(id) {
					t.Errorf("%s was not seeded", id)
				}
			}
			if config.FileExists() {
				t.Error("a fresh install wrote a config file")
			}
		})
	}
}

// TestActivePersonaAdoptionMatrix covers the rows about an install that already
// has profiles: which id an explicit choice, a stale choice and a bare default
// each land on.
func TestActivePersonaAdoptionMatrix(t *testing.T) {
	ada := userProfile("ada", "Ada", "kokoro", "af_sky")
	tests := []struct {
		name     string
		config   string
		profiles map[string]string
		want     string
	}{
		{
			name:     "row e: an explicit user persona is adopted",
			config:   "active_persona: ada\n",
			profiles: map[string]string{"ada": ada, "samantha": samanthaProfileOnDisk},
			want:     "ada",
		},
		{
			name:     "an explicit samantha that is missing is seeded, then adopted",
			config:   "active_persona: samantha\n",
			profiles: map[string]string{"ada": ada},
			want:     "samantha",
		},
		{
			name:     "an explicit obey that is missing is seeded, then adopted",
			config:   "active_persona: obey\n",
			profiles: map[string]string{"ada": ada, "samantha": samanthaProfileOnDisk},
			want:     "obey",
		},
		{
			name:     "the seeded default is adopted when it is ours",
			config:   "tts_provider: kokoro\n",
			profiles: map[string]string{"obey": builtinObeyOnDisk, "samantha": samanthaProfileOnDisk},
			want:     "obey",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newInstall(t)
			writeConfigFile(t, dir, tt.config)
			for id, body := range tt.profiles {
				writeProfileFile(t, dir, id, body)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.ActivePersona != tt.want {
				t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, tt.want)
			}
		})
	}
}

// TestFreshInstallLoadsAreIdempotent is matrix row h: nothing drifts on the
// second load, and no path invents a config file.
func TestFreshInstallLoadsAreIdempotent(t *testing.T) {
	newInstall(t)
	first, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	before := readAllProfiles(t)

	second, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	after := readAllProfiles(t)

	if first.ActivePersona != second.ActivePersona || first.AgentName != second.AgentName {
		t.Errorf("second load drifted: %q/%q then %q/%q",
			first.ActivePersona, first.AgentName, second.ActivePersona, second.AgentName)
	}
	if len(before) != len(after) {
		t.Fatalf("profile set changed: %d then %d", len(before), len(after))
	}
	for id, raw := range before {
		if after[id] != raw {
			t.Errorf("%s was rewritten on the second load", id)
		}
	}
	if config.FileExists() {
		t.Error("loading wrote a config file")
	}
}

func readAllProfiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range mustList(t) {
		raw, err := os.ReadFile(p.Path)
		if err != nil {
			t.Fatal(err)
		}
		out[p.ID] = string(raw)
	}
	return out
}
