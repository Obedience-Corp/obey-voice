package scene

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// appConfig is a stand-in for a loaded install: a Kokoro app default plus the
// live-conversation capabilities a scene has to switch off.
func appConfig() *config.Config {
	return &config.Config{
		TTSProvider:               "kokoro",
		TTSVoice:                  "af_heart",
		TTSPlace:                  "lan",
		BrainProvider:             "ollama",
		OllamaModel:               "qwen2.5:14b",
		ActivePersona:             "samantha",
		SkillsEnabled:             true,
		VoiceToolsEnabled:         true,
		EnvironmentContextEnabled: true,
	}
}

// resolved wraps a profile the way ResolveCast would, so BuildCast tests need
// nothing on disk.
func resolved(p *persona.Profile) ResolvedCast {
	return ResolvedCast{
		ID:          p.ID,
		PersonaID:   p.ID,
		DisplayName: p.DisplayName,
		Mouth:       Mouth{Provider: p.TTS.Provider, Voice: p.TTS.Voice, Tier: p.TTS.Tier},
		Profile:     p,
	}
}

func TestSceneBuildCastRejectsBadInput(t *testing.T) {
	loaded := resolved(profile("samantha", "Samantha", "kokoro", "af_heart", ""))
	unloaded := ResolvedCast{ID: "uncle", PersonaID: "uncle-fu"}

	if _, err := BuildCast(nil, []ResolvedCast{loaded}); err == nil {
		t.Error("BuildCast accepted a nil config")
	}
	if _, err := BuildCast(appConfig(), []ResolvedCast{loaded, unloaded}); err == nil {
		t.Error("BuildCast accepted a cast member whose persona was never loaded")
	}
}

// D17: applying a persona must not touch the caller's config. persona.Apply
// writes straight onto what it is given, and a scene applies a different
// persona for every cast member.
func TestSceneBuildCastLeavesCallerConfigAlone(t *testing.T) {
	cfg := appConfig()
	// An independently built baseline, not a shallow copy of cfg: a copy would
	// share the slice fields it is supposed to be guarding.
	before := appConfig()

	if _, err := BuildCast(cfg, []ResolvedCast{
		resolved(profile("samantha", "Samantha", "kokoro", "af_sky", "")),
		resolved(profile("uncle-fu", "Uncle Fu", "qwen3-tts", "Uncle_Fu", "1.7b")),
	}); err != nil {
		t.Fatalf("BuildCast: %v", err)
	}

	if !reflect.DeepEqual(cfg, before) {
		t.Errorf("BuildCast mutated the caller's config:\n got %+v\nwant %+v", *cfg, *before)
	}
}

// The settings a batch scene must never inherit from a live setup: LAN speech,
// tools, skills and the environment block that tells a model it has a computer.
func TestSceneBuildCastPinsBatchSettings(t *testing.T) {
	cast, err := BuildCast(appConfig(), []ResolvedCast{
		resolved(profile("samantha", "Samantha", "kokoro", "af_sky", "")),
		resolved(profile("uncle-fu", "Uncle Fu", "qwen3-tts", "Uncle_Fu", "1.7b")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}
	for _, member := range cast {
		applied := member.Config
		if applied.TTSPlace != config.TTSPlaceLocal {
			t.Errorf("%s tts_place = %q, want %q", member.ID, applied.TTSPlace, config.TTSPlaceLocal)
		}
		if applied.SkillsEnabled || applied.VoiceToolsEnabled || applied.EnvironmentContextEnabled {
			t.Errorf("%s still has skills/tools/environment enabled: %+v", member.ID, applied)
		}
	}
}

func TestSceneBuildCastSnapshotsTemplates(t *testing.T) {
	cfg := appConfig()
	cfg.QwenTTSLanguage = "English"
	cfg.QwenTTSMode = "customvoice"
	cfg.QwenTTSInstruction = "warm"

	cast, err := BuildCast(cfg, []ResolvedCast{
		resolved(profile("samantha", "Samantha", "", "", "")),
		resolved(profile("uncle-fu", "Uncle Fu", "qwen3-tts", "Uncle_Fu", "1.7")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}

	if got := cast[0].Template; got.Provider != "kokoro" || got.Voice != "af_heart" {
		t.Errorf("inherited template = %+v, want the app's kokoro/af_heart", got)
	}
	qwen := cast[1].Template
	if qwen.Provider != "qwen3-tts" || qwen.Voice != "Uncle_Fu" {
		t.Errorf("qwen template = %+v, want the persona's voice", qwen)
	}
	if qwen.Tier != "1.7b" {
		t.Errorf("tier = %q, want the normalized 1.7b", qwen.Tier)
	}
	if qwen.Language != "English" || qwen.Mode != "customvoice" || qwen.Instruction != "warm" {
		t.Errorf("qwen template dropped a shaping field: %+v", qwen)
	}
	if cast[0].BrainProvider != "ollama" || cast[0].BrainModel != "qwen2.5:14b" {
		t.Errorf("brain stack = %s/%s, want ollama/qwen2.5:14b", cast[0].BrainProvider, cast[0].BrainModel)
	}
}

// An empty Qwen voice is filled from the product default rather than left for
// the provider to resolve: a shared worker fills an empty request field from
// the persona that constructed it, which is the other speaker.
func TestSceneBuildCastFillsQwenDefaults(t *testing.T) {
	cast, err := BuildCast(appConfig(), []ResolvedCast{
		resolved(profile("a", "A", "qwen3-tts", "", "")),
		resolved(profile("b", "B", "qwen3-tts", "Vivian", "")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}
	if cast[0].Template.Voice == "" {
		t.Error("an inherited qwen voice was left empty for the shared worker to guess")
	}
	if cast[0].Template.Language == "" {
		t.Error("an inherited qwen language was left empty")
	}
}

// The PR1 review's deferred note: the collision that matters is between the
// EFFECTIVE mouths. One persona naming a voice the other inherits from the app
// defaults is one voice reading both parts, and no comparison of the declared
// persona fields can see it.
func TestSceneSharedVoicesCatchesInheritedCollision(t *testing.T) {
	cases := map[string]struct {
		personas [2]*persona.Profile
		want     bool
	}{
		"distinct voices": {
			[2]*persona.Profile{
				profile("samantha", "Samantha", "kokoro", "af_sky", ""),
				profile("uncle-fu", "Uncle Fu", "kokoro", "am_michael", ""),
			}, false,
		},
		"both declare the same voice": {
			[2]*persona.Profile{
				profile("samantha", "Samantha", "kokoro", "af_heart", ""),
				profile("uncle-fu", "Uncle Fu", "kokoro", "af_heart", ""),
			}, true,
		},
		"one names the app default the other inherits": {
			[2]*persona.Profile{
				profile("samantha", "Samantha", "kokoro", "af_heart", ""),
				profile("uncle-fu", "Uncle Fu", "", "", ""),
			}, true,
		},
		"both inherit": {
			[2]*persona.Profile{
				profile("samantha", "Samantha", "", "", ""),
				profile("uncle-fu", "Uncle Fu", "", "", ""),
			}, true,
		},
		"same voice id on different providers": {
			[2]*persona.Profile{
				profile("samantha", "Samantha", "kokoro", "Vivian", ""),
				profile("uncle-fu", "Uncle Fu", "qwen3-tts", "Vivian", ""),
			}, false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cast, err := BuildCast(appConfig(), []ResolvedCast{resolved(tc.personas[0]), resolved(tc.personas[1])})
			if err != nil {
				t.Fatalf("BuildCast: %v", err)
			}
			warnings := SharedVoices(cast)
			if got := len(warnings) > 0; got != tc.want {
				t.Fatalf("SharedVoices = %v (%v), want collision=%v", warnings, got, tc.want)
			}
			if tc.want && !strings.Contains(warnings[0], tc.personas[0].ID) {
				t.Errorf("warning %q does not name the colliding speakers", warnings[0])
			}
		})
	}
}

// The declared comparison the plan runner uses is deliberately weaker: it is
// the one that misses an inherited collision, which is why the recording path
// recomputes.
func TestSceneDeclaredMouthsMissInheritedCollision(t *testing.T) {
	cast := []ResolvedCast{
		resolved(profile("samantha", "Samantha", "kokoro", "af_heart", "")),
		resolved(profile("uncle-fu", "Uncle Fu", "", "", "")),
	}
	if warnings := declaredSharedMouths(cast); len(warnings) != 0 {
		t.Fatalf("declared comparison reported %v; the effective one is what catches this", warnings)
	}
}

func TestSceneValidatePool(t *testing.T) {
	cases := map[string]struct {
		personas [2]*persona.Profile
		wantErr  bool
	}{
		"two kokoro voices":        {[2]*persona.Profile{profile("a", "A", "kokoro", "af_sky", ""), profile("b", "B", "kokoro", "am_michael", "")}, false},
		"kokoro and qwen":          {[2]*persona.Profile{profile("a", "A", "kokoro", "af_sky", ""), profile("b", "B", "qwen3-tts", "Vivian", "1.7b")}, false},
		"two qwen voices one tier": {[2]*persona.Profile{profile("a", "A", "qwen3-tts", "Vivian", "0.6b"), profile("b", "B", "qwen3-tts", "Uncle_Fu", "0.6b")}, false},
		"one qwen tier inherited":  {[2]*persona.Profile{profile("a", "A", "qwen3-tts", "Vivian", ""), profile("b", "B", "qwen3-tts", "Uncle_Fu", "0.6b")}, false},
		"two qwen tiers":           {[2]*persona.Profile{profile("a", "A", "qwen3-tts", "Vivian", "0.6b"), profile("b", "B", "qwen3-tts", "Uncle_Fu", "1.7b")}, true},
		// A provider that ignores the per-turn voice would read both parts in
		// one voice while the cast still looked like two mouths.
		"two single-voice mouths": {[2]*persona.Profile{profile("a", "A", "audio8", "", ""), profile("b", "B", "audio8", "", "")}, true},
		"one single-voice mouth":  {[2]*persona.Profile{profile("a", "A", "audio8", "", ""), profile("b", "B", "kokoro", "af_sky", "")}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cast, err := BuildCast(appConfig(), []ResolvedCast{resolved(tc.personas[0]), resolved(tc.personas[1])})
			if err != nil {
				t.Fatalf("BuildCast: %v", err)
			}
			err = ValidatePool(cast)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidatePool = %v, want error=%v", err, tc.wantErr)
			}
		})
	}
}

// One worker per unique mouth engine: two Kokoro voices share a provider, and a
// mixed cast constructs both.
func TestScenePoolPlanDeduplicates(t *testing.T) {
	cases := map[string]struct {
		personas [2]*persona.Profile
		want     int
	}{
		"two kokoro voices":        {[2]*persona.Profile{profile("a", "A", "kokoro", "af_sky", ""), profile("b", "B", "kokoro", "am_michael", "")}, 1},
		"two qwen voices one tier": {[2]*persona.Profile{profile("a", "A", "qwen3-tts", "Vivian", "0.6b"), profile("b", "B", "qwen3-tts", "Uncle_Fu", "0.6b")}, 1},
		"kokoro and qwen":          {[2]*persona.Profile{profile("a", "A", "kokoro", "af_sky", ""), profile("b", "B", "qwen3-tts", "Vivian", "0.6b")}, 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cast, err := BuildCast(appConfig(), []ResolvedCast{resolved(tc.personas[0]), resolved(tc.personas[1])})
			if err != nil {
				t.Fatalf("BuildCast: %v", err)
			}
			if got := len(PoolPlan(cast)); got != tc.want {
				t.Fatalf("PoolPlan constructs %d worker(s), want %d", got, tc.want)
			}
		})
	}
}

// An unset Kokoro voice is resolved here, not left for the provider. Left
// empty it would reach a pooled worker and fall back to whichever cast member
// constructed it, so B would speak in A's voice while the two mouths still
// compared as distinct.
func TestSceneBuildCastFillsKokoroDefault(t *testing.T) {
	cfg := appConfig()
	cfg.TTSVoice = "" // an explicit tts_voice: "" in the operator's config

	cast, err := BuildCast(cfg, []ResolvedCast{
		resolved(profile("a", "A", "kokoro", "af_sky", "")),
		resolved(profile("b", "B", "kokoro", "", "")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}
	if cast[1].Template.Voice == "" {
		t.Fatal("an inherited kokoro voice was left empty for the shared worker to guess")
	}
	if cast[1].Template.Voice != defaultKokoroVoice {
		t.Errorf("inherited voice = %q, want the kokoro default %q", cast[1].Template.Voice, defaultKokoroVoice)
	}

	// Two personas that both inherit the empty default now compare as one
	// mouth, which is what they are.
	both, err := BuildCast(cfg, []ResolvedCast{
		resolved(profile("a", "A", "kokoro", "", "")),
		resolved(profile("b", "B", "", "", "")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}
	if len(SharedVoices(both)) != 1 {
		t.Errorf("two inherited kokoro voices did not compare as one mouth: %+v", both)
	}
}

// A provider this package does not know a default for keeps the config value
// verbatim rather than being stamped with Kokoro's.
func TestSceneBuildCastLeavesUnknownProviderVoiceAlone(t *testing.T) {
	cfg := appConfig()
	cfg.TTSVoice = ""

	cast, err := BuildCast(cfg, []ResolvedCast{
		resolved(profile("a", "A", "audio8", "", "")),
		resolved(profile("b", "B", "kokoro", "af_sky", "")),
	})
	if err != nil {
		t.Fatalf("BuildCast: %v", err)
	}
	if cast[0].Template.Voice != "" {
		t.Errorf("audio8 voice = %q, want the config value verbatim", cast[0].Template.Voice)
	}
}

// The pool knows which providers can tell two speakers apart.
func TestScenePoolKeySpeaksOneVoice(t *testing.T) {
	cases := map[string]struct {
		key  PoolKey
		want bool
	}{
		"kokoro":         {PoolKey{Provider: "kokoro"}, false},
		"qwen3-tts":      {PoolKey{Provider: "qwen3-tts", Tier: "0.6b"}, false},
		"audio8":         {PoolKey{Provider: "audio8"}, true},
		"something else": {PoolKey{Provider: "future-tts"}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.key.SpeaksOneVoice(); got != tc.want {
				t.Fatalf("SpeaksOneVoice() = %v, want %v", got, tc.want)
			}
		})
	}
}
