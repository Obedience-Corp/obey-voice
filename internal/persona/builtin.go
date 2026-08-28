package persona

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// builtinIDs are the profiles the product ships. Both are protected from
// deletion (see Delete): obey because every heal path falls back to it, and
// samantha because installs that predate the rebrand are bound to her.
var builtinIDs = []string{DefaultID, LegacyDefaultID}

// IsBuiltinID reports whether id names a shipped profile.
func IsBuiltinID(id string) bool {
	id = strings.TrimSpace(id)
	for _, b := range builtinIDs {
		if id == b {
			return true
		}
	}
	return false
}

// Builtins returns the profiles a fresh install starts with, default first.
//
// obey pins nothing — no brain provider, no model, no TTS provider, voice or
// tier — so the default persona always speaks with whatever the app config
// says. Every field it left empty inherits at Apply time, which is what makes
// "the default model settings" mean today's settings rather than the ones that
// happened to be current when the profile was written.
//
// samantha is the profile a pre-rebrand fresh install would have been given:
// her own prompt document and the app's TTS stack snapshotted onto the profile,
// exactly as FromConfig produced it. She is an ordinary persona now — seeded,
// listed, selectable — just not the default.
func Builtins(cfg *config.Config) []*Profile {
	if cfg == nil {
		cfg = &config.Config{}
	}
	display := strings.TrimSpace(cfg.AgentName)
	if display == "" {
		display = DefaultDisplayName
	}
	obey := &Profile{
		Schema:      Schema,
		ID:          DefaultID,
		DisplayName: display,
		Builtin:     true,
		Prompts:     PromptRefs{Persona: DefaultID},
	}

	legacy := FromConfig(cfg)
	samantha := &Profile{
		Schema:      Schema,
		ID:          LegacyDefaultID,
		DisplayName: LegacyDefaultDisplayName,
		Builtin:     true,
		TTS:         legacy.TTS,
		Prompts:     PromptRefs{Persona: LegacyDefaultID},
	}
	return []*Profile{obey, samantha}
}

// SeedBuiltins writes the shipped profiles that are not on disk yet. It never
// overwrites: a user who edited obey's voice or renamed samantha keeps their
// file.
func SeedBuiltins(cfg *config.Config) error {
	for _, p := range Builtins(cfg) {
		if err := Write(p, true); err != nil {
			return fmt.Errorf("seeding built-in persona %q: %w", p.ID, err)
		}
	}
	return nil
}
