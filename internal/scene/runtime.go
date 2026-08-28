package scene

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// TTS provider names this renderer reasons about by name. Kokoro is the empty
// default; qwen3-tts is the one whose model tier decides how many workers a
// cast needs.
const (
	providerKokoro = "kokoro"
	providerQwen   = "qwen3-tts"
)

// defaultKokoroVoice is what an unset tts_voice speaks as. The value is
// duplicated rather than imported because neither source exports it: it is the
// viper default (config setDefaults) and, independently, Kokoro's own fallback
// for a voice it does not recognize (NewKokoroTTS). Naming it here is what
// keeps an empty voice from reaching a pooled worker, where it would fall back
// to whichever cast member happened to construct that worker.
const defaultKokoroVoice = "af_heart"

// SynthTemplate is everything one cast member needs to speak a line. Every
// field is a string because internal/scene must not import
// pkg/voiceagent/tts — the command layer maps these onto a typed request.
//
// The template travels with each turn instead of being bound when a provider is
// constructed. Two personas routinely share one worker (that is the point of
// the pool), and a provider filling empty request fields from its construction
// config would speak the first cast member's voice for the second one's lines.
type SynthTemplate struct {
	Provider            string
	Tier                string
	Voice               string
	Language            string
	Mode                string
	Instruction         string
	ReferenceAudio      string
	ReferenceTranscript string
}

// Mouth is the effective voice identity this template speaks with.
func (t SynthTemplate) Mouth() Mouth {
	return Mouth{Provider: t.Provider, Voice: t.Voice, Tier: t.Tier}
}

// CastRuntime is one cast member after their persona has been applied to its
// own copy of the app config: the stack this run will actually use, as opposed
// to the fields the persona document happens to declare.
type CastRuntime struct {
	ID            string // scene-local speaker id
	PersonaID     string
	DisplayName   string
	BrainProvider string
	BrainModel    string
	Template      SynthTemplate

	// Config is this member's applied config copy. The command layer ensures
	// assets and constructs the provider from it; nothing here mutates it.
	Config *config.Config
}

// BuildCast applies every cast member's persona to its own copy of cfg and
// snapshots the effective stack.
//
// The caller's config is never touched. persona.Apply writes the display name,
// prompt refs, brain routing, TTS keys and the in-memory active persona
// straight onto the config it is given, and a scene switches persona on every
// turn — so each member gets a copy, and persona.Use (which would persist the
// switch) is never called.
func BuildCast(cfg *config.Config, cast []ResolvedCast) ([]CastRuntime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scene: no config")
	}
	runtimes := make([]CastRuntime, 0, len(cast))
	for _, member := range cast {
		if member.Profile == nil {
			return nil, fmt.Errorf("scene: cast %q: persona %q was not loaded", member.ID, member.PersonaID)
		}
		applied := applyPersonaCopy(cfg, member.Profile)
		runtimes = append(runtimes, CastRuntime{
			ID:            member.ID,
			PersonaID:     member.PersonaID,
			DisplayName:   member.DisplayName,
			BrainProvider: strings.TrimSpace(applied.BrainProvider),
			BrainModel:    persona.ModelForProvider(applied, applied.BrainProvider),
			Template:      templateFor(applied),
			Config:        applied,
		})
	}
	return runtimes, nil
}

// applyPersonaCopy overlays a persona onto a copy of cfg and pins the settings
// a batch scene must not inherit from the operator's live setup.
func applyPersonaCopy(cfg *config.Config, profile *persona.Profile) *config.Config {
	applied := *cfg
	persona.Apply(&applied, profile)
	// A scene records here. Voice cloning cannot cross to a speech worker on
	// another computer, and a recording that silently moved machines would not
	// be reproducible from its manifest.
	applied.TTSPlace = config.TTSPlaceLocal
	// Batch dialogue has no tools to run, no skills to load, and no environment
	// worth describing. Disabled *after* Apply: applying a persona's brain
	// re-runs the ollama defaults, which turn tools and skills back on.
	applied.SkillsEnabled = false
	applied.VoiceToolsEnabled = false
	applied.EnvironmentContextEnabled = false
	return &applied
}

// templateFor snapshots the per-turn synthesis settings from an applied config.
//
// An empty voice is resolved to the provider's default here rather than left to
// the provider itself, because there an empty field falls back to whichever
// cast member happened to construct the shared worker — cast member B would
// speak in cast member A's voice, and the mouths would still compare as
// distinct. A provider this package does not know the default for keeps the
// config value verbatim; ValidatePool refuses to seat two speakers behind it.
func templateFor(cfg *config.Config) SynthTemplate {
	provider := normalizeProvider(cfg.TTSProvider)
	if provider != providerQwen {
		voice := strings.TrimSpace(cfg.TTSVoice)
		if provider == providerKokoro {
			voice = firstNonBlank(voice, defaultKokoroVoice)
		}
		return SynthTemplate{Provider: provider, Voice: voice}
	}
	return SynthTemplate{
		Provider:            provider,
		Tier:                managedqwen.NormalizeModelTier(cfg.QwenTTSModelTier),
		Voice:               firstNonBlank(cfg.QwenTTSVoice, managedqwen.DefaultVoice),
		Language:            firstNonBlank(cfg.QwenTTSLanguage, managedqwen.DefaultLanguage),
		Mode:                strings.TrimSpace(cfg.QwenTTSMode),
		Instruction:         strings.TrimSpace(cfg.QwenTTSInstruction),
		ReferenceAudio:      strings.TrimSpace(cfg.QwenTTSReferenceAudio),
		ReferenceTranscript: strings.TrimSpace(cfg.QwenTTSReferenceText),
	}
}

// SharedVoices reports each group of cast members that would speak with the
// same effective mouth, in cast order.
//
// This is the check that matters: two personas collide when their *resolved*
// provider and voice match, which happens whenever one names a voice the other
// inherits from the app defaults — a pairing no comparison of the declared
// persona fields can see.
func SharedVoices(cast []CastRuntime) []string {
	ids := make([]string, 0, len(cast))
	mouths := make([]Mouth, 0, len(cast))
	for _, member := range cast {
		ids = append(ids, member.ID)
		mouths = append(mouths, member.Template.Mouth())
	}
	return sharedMouths(ids, mouths)
}

func normalizeProvider(provider string) string {
	if provider = strings.ToLower(strings.TrimSpace(provider)); provider != "" {
		return provider
	}
	return providerKokoro
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
