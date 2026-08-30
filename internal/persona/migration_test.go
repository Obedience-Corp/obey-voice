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
const builtinObeyOnDisk = `# yaml-language-server: $schema=festival-voice.persona.v1
schema: festival-voice.persona.v1
id: obey
display_name: Obey
builtin: true
tts: {}
prompts:
    persona: obey
`

const samanthaProfileOnDisk = `# yaml-language-server: $schema=festival-voice.persona.v1
schema: festival-voice.persona.v1
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

	// Capture the app-level stack before any persona exists, so "obey pins
	// nothing" can be checked against what the config would have said anyway.
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
	if p.Brain != (Brain{}) || p.TTS != (TTS{}) {
		t.Errorf("obey pins a stack: brain %+v tts %+v — the default persona must inherit the app config", p.Brain, p.TTS)
	}

	// Inheriting means the app-level stack survives the overlay untouched.
	if cfg.TTSProvider != before.TTSProvider || cfg.TTSVoice != before.TTSVoice ||
		cfg.QwenTTSVoice != before.QwenTTSVoice || cfg.QwenTTSModelTier != before.QwenTTSModelTier ||
		cfg.BrainProvider != before.BrainProvider || cfg.OllamaModel != before.OllamaModel {
		t.Errorf("obey changed the app stack: tts %q/%q brain %q/%q, want %q/%q %q/%q",
			cfg.TTSProvider, cfg.TTSVoice, cfg.BrainProvider, cfg.OllamaModel,
			before.TTSProvider, before.TTSVoice, before.BrainProvider, before.OllamaModel)
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
// still ships — a fresh install can switch to her without downloading anything.
func TestFreshInstallStillSeedsSamantha(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	p, err := Load(LegacyDefaultID)
	if err != nil {
		t.Fatalf("Load(%s): %v", LegacyDefaultID, err)
	}
	if p.DisplayName != LegacyDefaultDisplayName || !p.Builtin {
		t.Errorf("samantha profile = %+v, want display %q and builtin", p, LegacyDefaultDisplayName)
	}
	// Her voice keeps being pinned from the app defaults, exactly as the
	// pre-rebrand seeding produced it.
	if p.TTS.Provider != cfg.TTSProvider || p.TTS.Voice == "" {
		t.Errorf("samantha tts = %+v, want the app default provider %q and a voice", p.TTS, cfg.TTSProvider)
	}

	text, err := LoadSystemPrompt(LegacyDefaultID)
	if err != nil {
		t.Fatalf("LoadSystemPrompt(samantha): %v", err)
	}
	if !strings.Contains(text, "inspired by the character from the movie Her") {
		t.Errorf("samantha prompt is not her identity:\n%s", text)
	}

	// Switching to her carries her identity onto the config.
	if err := Use(cfg, LegacyDefaultID); err != nil {
		t.Fatalf("Use(samantha): %v", err)
	}
	if cfg.ActivePersona != LegacyDefaultID || cfg.AgentName != LegacyDefaultDisplayName || cfg.Persona != LegacyDefaultID {
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

	p, err := Load(VeronicaID)
	if err != nil {
		t.Fatalf("Load(%s): %v", VeronicaID, err)
	}
	if p.DisplayName != VeronicaDisplayName || !p.Builtin {
		t.Errorf("veronica profile = %+v, want display %q and builtin", p, VeronicaDisplayName)
	}
	if p.TTS.Provider != "qwen3-tts" || p.TTS.Voice != "VeronicaDesk" || p.TTS.Tier != "0.6b" {
		t.Errorf("veronica tts = %+v, want qwen3-tts VeronicaDesk 0.6b", p.TTS)
	}
	if p.Brain != (Brain{}) {
		t.Errorf("veronica brain = %+v, want inherited", p.Brain)
	}

	text, err := LoadSystemPrompt(VeronicaID)
	if err != nil {
		t.Fatalf("LoadSystemPrompt(veronica): %v", err)
	}
	if !strings.Contains(text, "inspired by the character from the movie Her") {
		t.Errorf("veronica prompt is not her public identity:\n%s", text)
	}

	if err := Use(cfg, VeronicaID); err != nil {
		t.Fatalf("Use(veronica): %v", err)
	}
	if cfg.ActivePersona != VeronicaID || cfg.AgentName != VeronicaDisplayName || cfg.Persona != VeronicaID {
		t.Errorf("after Use(veronica): active %q name %q prompt %q", cfg.ActivePersona, cfg.AgentName, cfg.Persona)
	}
	if cfg.TTSProvider != "qwen3-tts" || cfg.QwenTTSVoice != "VeronicaDesk" || cfg.QwenTTSModelTier != "0.6b" {
		t.Errorf("after Use(veronica): tts %q voice %q tier %q", cfg.TTSProvider, cfg.QwenTTSVoice, cfg.QwenTTSModelTier)
	}
}

// TestSeedBuiltinsLeavesAUserVeronicaAlone is the privacy contract: a
// user-authored profile at id veronica is not replaced by the shipped one.
func TestSeedBuiltinsLeavesAUserVeronicaAlone(t *testing.T) {
	dir := newInstall(t)
	const private = `# yaml-language-server: $schema=festival-voice.persona.v1
schema: festival-voice.persona.v1
id: veronica
display_name: Veronica
builtin: false
tts:
    provider: kokoro
    voice: af_sky
prompts:
    persona: veronica
`
	path := writeProfileFile(t, dir, VeronicaID, private)
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
	p, err := Load(VeronicaID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Builtin || p.TTS.Voice != "af_sky" {
		t.Errorf("veronica = %+v, want the user profile left in place", p)
	}
}

// TestExistingSamanthaInstallIsUnchanged is the migration contract: a config
// that names samantha, on an install that already has her profile, loads
// exactly what it loaded before the default moved — id, name, prompt, pinned
// kokoro voice — and its persona directory is not added to or rewritten.
func TestExistingSamanthaInstallIsUnchanged(t *testing.T) {
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

	if ids := loadIDs(t); len(ids) != 1 || ids[0] != "samantha" {
		t.Errorf("personas = %v, want only samantha — an upgrade must not inject profiles", ids)
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

// TestExistingInstallWithoutActivePersonaKeepsItsPersona covers the install
// that never ran `persona use`: active_persona is only the shipped default, so
// after the rebrand it points at a profile that install does not have. It must
// heal onto what it already had, not error and not silently become obey.
func TestExistingInstallWithoutActivePersonaKeepsItsPersona(t *testing.T) {
	tests := []struct {
		name     string
		profiles map[string]string
		want     string
	}{
		{
			name:     "samantha only",
			profiles: map[string]string{"samantha": samanthaProfileOnDisk},
			want:     "samantha",
		},
		{
			name: "samantha beside a user persona",
			profiles: map[string]string{
				"samantha": samanthaProfileOnDisk,
				"ada":      "schema: festival-voice.persona.v1\nid: ada\ndisplay_name: Ada\nprompts:\n  persona: ada\n",
			},
			// ada sorts first; the pre-rebrand default still wins.
			want: "samantha",
		},
		{
			name: "no built-in profile at all",
			profiles: map[string]string{
				"ada": "schema: festival-voice.persona.v1\nid: ada\ndisplay_name: Ada\nprompts:\n  persona: ada\n",
			},
			want: "ada",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newInstall(t)
			writeConfigFile(t, dir, "tts_provider: kokoro\n")
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
			if len(loadIDs(t)) != len(tt.profiles) {
				t.Errorf("personas = %v, want the %d already installed", loadIDs(t), len(tt.profiles))
			}
		})
	}
}

// TestLegacySingleAgentMigration covers installs that predate the personas
// directory: their identity lives in config keys and migrates into one profile.
// Which profile is decided by what the config names, never by today's default.
func TestLegacySingleAgentMigration(t *testing.T) {
	tests := []struct {
		name string
		// userPrompt seeds prompts/persona/<name>.yaml the way a pre-personas
		// install that authored its own identity already has one.
		userPrompt    string
		config        string
		wantActive    string
		wantName      string
		wantVoice     string
		wantProfile   []string
		wantPromptRef string
		wantPrompt    string
		// wantPins/wantOwned are only checked for the rows that set them: a
		// migrated profile the user owns must keep its pins and stay deletable.
		wantPins  TTS
		wantOwned bool
	}{
		{
			name:        "pre-personas samantha keeps her name and voice",
			config:      "persona: samantha\nagent_name: Samantha\ntts_provider: kokoro\ntts_voice: af_nova\n",
			wantActive:  "samantha",
			wantName:    "Samantha",
			wantVoice:   "af_nova",
			wantProfile: []string{"samantha"},
		},
		{
			name:        "pre-personas custom prompt name becomes the profile id",
			userPrompt:  "festival",
			config:      "persona: festival\nagent_name: Festival Bot\ntts_provider: kokoro\ntts_voice: af_bella\n",
			wantActive:  "festival",
			wantName:    "Festival Bot",
			wantVoice:   "af_bella",
			wantProfile: []string{"festival"},
		},
		{
			name:        "active_persona alone still migrates that persona",
			config:      "active_persona: samantha\nagent_name: Samantha\ntts_provider: kokoro\ntts_voice: af_heart\n",
			wantActive:  "samantha",
			wantName:    "Samantha",
			wantVoice:   "af_heart",
			wantProfile: []string{"samantha"},
		},
		{
			name:        "a non-slug prompt name migrates under the pre-personas id",
			userPrompt:  "uncle-fu",
			config:      "persona: Uncle_Fu\nagent_name: Uncle Fu\ntts_provider: kokoro\ntts_voice: af_heart\n",
			wantActive:  "samantha",
			wantName:    "Uncle Fu",
			wantVoice:   "af_heart",
			wantProfile: []string{"samantha"},
			// The stale ref is preserved, not repointed at the new default;
			// LoadSystemPrompt then resolves it through the profile's own id.
			wantPromptRef: "Uncle_Fu",
		},
		{
			// Matrix row d2, the one the first review caught flipping. The
			// config writer is surgical, so a pre-personas install holds only
			// the keys someone set — and almost nobody set persona. A missing
			// key must not read as "fresh".
			name:        "no persona key at all still migrates Samantha",
			config:      "agent_name: Samantha\ntts_provider: kokoro\ntts_voice: af_heart\nbrain_provider: ollama\n",
			wantActive:  LegacyDefaultID,
			wantName:    LegacyDefaultDisplayName,
			wantVoice:   "af_heart",
			wantProfile: []string{LegacyDefaultID},
			wantPrompt:  "inspired by the character from the movie Her",
		},
		{
			// Row d2b: no agent_name either. Under the broken gate this was a
			// clean name-and-prompt flip to Obey.
			name:        "no identity keys at all still migrates Samantha",
			config:      "tts_provider: kokoro\ntts_voice: af_heart\n",
			wantActive:  LegacyDefaultID,
			wantName:    LegacyDefaultDisplayName,
			wantVoice:   "af_heart",
			wantProfile: []string{LegacyDefaultID},
			wantPrompt:  "inspired by the character from the movie Her",
		},
		{
			// Finding 6 / row B: the config names a user-authored obey
			// document. The migrated profile is theirs — it keeps their pins
			// and stays deletable, because we did not write it.
			name:        "a config naming obey migrates the user's own profile",
			config:      "persona: obey\nagent_name: Custom\ntts_provider: kokoro\ntts_voice: af_sky\n",
			wantActive:  DefaultID,
			wantName:    "Custom",
			wantVoice:   "af_sky",
			wantProfile: []string{DefaultID},
			wantOwned:   false,
			wantPins:    TTS{Provider: "kokoro", Voice: "af_sky"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newInstall(t)
			writeConfigFile(t, dir, tt.config)
			if tt.userPrompt != "" {
				if err := WriteSystemPrompt(tt.userPrompt, "You are {agent_name}."); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.ActivePersona != tt.wantActive {
				t.Errorf("ActivePersona = %q, want %q", cfg.ActivePersona, tt.wantActive)
			}
			if cfg.AgentName != tt.wantName {
				t.Errorf("AgentName = %q, want %q", cfg.AgentName, tt.wantName)
			}
			if cfg.TTSVoice != tt.wantVoice {
				t.Errorf("TTSVoice = %q, want %q", cfg.TTSVoice, tt.wantVoice)
			}
			got := loadIDs(t)
			if len(got) != len(tt.wantProfile) {
				t.Fatalf("personas = %v, want %v", got, tt.wantProfile)
			}
			for _, want := range tt.wantProfile {
				if !hasProfileID(mustList(t), want) {
					t.Errorf("personas = %v, missing %q", got, want)
				}
			}
			if tt.wantPromptRef != "" {
				p, err := Load(tt.wantActive)
				if err != nil {
					t.Fatal(err)
				}
				if p.Prompts.Persona != tt.wantPromptRef {
					t.Errorf("prompts.persona = %q, want the config's own ref %q", p.Prompts.Persona, tt.wantPromptRef)
				}
			}
			p, err := Load(tt.wantActive)
			if err != nil {
				t.Fatalf("Load(%s): %v", tt.wantActive, err)
			}
			if p.Builtin != tt.wantOwned && (tt.wantOwned || p.ID == DefaultID) {
				t.Errorf("%s builtin = %v, want %v — provenance, not id, decides ownership", p.ID, p.Builtin, tt.wantOwned)
			}
			if (tt.wantPins != TTS{}) && p.TTS != tt.wantPins {
				t.Errorf("%s tts = %+v, want the config's own pins %+v", p.ID, p.TTS, tt.wantPins)
			}
			if tt.wantPrompt != "" {
				text, err := LoadSystemPrompt(cfg.Persona)
				if err != nil {
					t.Fatalf("LoadSystemPrompt(%s): %v", cfg.Persona, err)
				}
				if !strings.Contains(text, tt.wantPrompt) {
					t.Errorf("resolved prompt does not contain %q:\n%s", tt.wantPrompt, text)
				}
			}
			if tt.wantPromptRef == "" {
				// Whatever was migrated, its prompt must resolve. Skipped for
				// the stale-ref case: that ref did not resolve before the
				// rebrand either (it is the Uncle_Fu footgun effectivePersonaRef
				// exists to survive), and preserving it verbatim is the point.
				if _, err := LoadSystemPrompt(cfg.Persona); err != nil {
					t.Errorf("LoadSystemPrompt(%s): %v", cfg.Persona, err)
				}
			}
		})
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
			writeProfileFile(t, dir, "ada", "schema: festival-voice.persona.v1\nid: ada\ndisplay_name: Ada\nprompts:\n  persona: ada\n")
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

// TestBuiltinsPinNothingButSamanthasVoice pins the shape of the seeded
// profiles directly, independent of any load path.
func TestBuiltinsPinNothingButSamanthasVoice(t *testing.T) {
	cfg := &config.Config{
		AgentName:     "Obey",
		BrainProvider: "ollama",
		OllamaModel:   "qwen3:14b",
		TTSProvider:   "kokoro",
		TTSVoice:      "af_heart",
	}
	got := Builtins(cfg)
	if len(got) != 3 || got[0].ID != DefaultID || got[1].ID != LegacyDefaultID || got[2].ID != VeronicaID {
		t.Fatalf("Builtins() = %+v, want obey, samantha, veronica", got)
	}
	if got[0].Brain != (Brain{}) || got[0].TTS != (TTS{}) {
		t.Errorf("obey pins %+v / %+v, want everything inherited", got[0].Brain, got[0].TTS)
	}
	if got[0].Prompts.Persona != DefaultID || !got[0].Builtin {
		t.Errorf("obey profile = %+v, want its own prompt and builtin", got[0])
	}
	if got[1].TTS.Provider != "kokoro" || got[1].TTS.Voice != "af_heart" {
		t.Errorf("samantha tts = %+v, want the app stack snapshotted", got[1].TTS)
	}
	if got[1].Brain != (Brain{}) {
		t.Errorf("samantha brain = %+v, want inherited (she never pinned one)", got[1].Brain)
	}
	if got[2].TTS.Provider != "qwen3-tts" || got[2].TTS.Voice != "VeronicaDesk" || got[2].TTS.Tier != "0.6b" {
		t.Errorf("veronica tts = %+v, want qwen3-tts VeronicaDesk 0.6b even on a kokoro install", got[2].TTS)
	}
	if got[2].Brain != (Brain{}) {
		t.Errorf("veronica brain = %+v, want inherited so she does not force an ollama model", got[2].Brain)
	}
	if got[2].Prompts.Persona != VeronicaID || !got[2].Builtin {
		t.Errorf("veronica profile = %+v, want its own prompt and builtin", got[2])
	}
}

// TestBuiltinsAreUndeletable: obey because every heal path falls back to it,
// samantha because that is exactly what she was before the rebrand, veronica
// because she is a shipped companion and not a user-authored profile.
func TestBuiltinsAreUndeletable(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{DefaultID, LegacyDefaultID, VeronicaID} {
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
	return "schema: festival-voice.persona.v1\nid: " + id + "\ndisplay_name: " + display +
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
			// profile it names is a collision, not a choice.
			name:       "defaulted active_persona skips the user's obey",
			config:     "tts_provider: kokoro\n",
			wantActive: "samantha",
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
	want := []string{"samantha"}
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
			env:        map[string]string{"ACTIVE_PERSONA": LegacyDefaultID},
			wantActive: LegacyDefaultID,
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
			for _, id := range []string{DefaultID, LegacyDefaultID, VeronicaID} {
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
			name:     "row g: an explicit samantha that is missing heals",
			config:   "active_persona: samantha\n",
			profiles: map[string]string{"ada": ada},
			want:     "ada",
		},
		{
			name:     "an explicit obey that is missing heals to the shipped samantha",
			config:   "active_persona: obey\n",
			profiles: map[string]string{"ada": ada, "samantha": samanthaProfileOnDisk},
			want:     "samantha",
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
