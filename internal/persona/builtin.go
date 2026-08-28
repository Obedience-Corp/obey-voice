package persona

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// builtinIDs are the ids the product ships profiles under. Membership is about
// the NAME, not about ownership: a profile at one of these ids is ours only
// when its own builtin flag says we wrote it (see Owned). Nothing may be
// inferred about a file on disk from its id alone — users create personas, and
// "obey" is a name a user can pick.
var builtinIDs = []string{DefaultID, LegacyDefaultID}

// IsBuiltinID reports whether id is one the product ships a profile under.
func IsBuiltinID(id string) bool {
	id = strings.TrimSpace(id)
	for _, b := range builtinIDs {
		if id == b {
			return true
		}
	}
	return false
}

// Owned reports whether p is a profile this code wrote, rather than one the
// user authored that happens to sit at a shipped id. The flag on disk is the
// provenance record: Create always writes builtin:false, seeding writes true,
// and neither ever overwrites an existing file.
func Owned(p *Profile) bool {
	return p != nil && p.Builtin
}

// Builtins returns the profiles a fresh install starts with, default first.
//
// obey pins nothing — no brain provider, no model, no TTS provider, voice or
// tier — so the default persona always speaks with whatever the app config
// says. Every field it left empty inherits at Apply time, which is what makes
// "the default model settings" mean today's settings rather than the ones that
// happened to be current when the profile was written. Its display name is the
// one thing it must carry, since a profile without one is invalid; it comes
// from agent_name, which is "Obey" unless this install named the agent.
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

// SeedBuiltins writes the shipped profiles whose slots are free.
//
// It never writes over an occupied slot. A user persona named obey keeps its
// file, its pins and its deletability; the shipped default simply does not
// exist on that install, and the fallback order in pickFallbackID takes it from
// there. Stamping builtin:true onto someone else's profile would make it
// undeletable and depin its voice — taking ownership of a file we did not
// write.
func SeedBuiltins(cfg *config.Config) error {
	for _, p := range Builtins(cfg) {
		if err := Write(p, true); err != nil {
			return fmt.Errorf("seeding built-in persona %q: %w", p.ID, err)
		}
	}
	return nil
}
