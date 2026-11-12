package cmd

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// configPersonaPayload is the persona block of `config get --json`. It names
// the keys the default persona for new conversations overrides rather than
// replacing their values, so a front end can badge a control without lying
// about what the file holds.
type configPersonaPayload struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Overrides   []string `json:"overrides"`
}

// personaOverlay reports the default persona for new conversations and the
// keys it overrides, or nil when there is none, no profile on disk, or the
// profile is unreadable. It runs outside any conversation, so it cannot know
// which persona a running one is speaking as. A broken persona must never
// break Settings, so every failure is reported as "no overlay" rather than as
// an error.
//
// The profile is loaded read-only: no EnsureAndApply, no migration. `config
// get` is contractually free of writes.
func personaOverlay() *configPersonaPayload {
	cfg, err := config.LoadRaw()
	if err != nil {
		return nil
	}
	// LoadRaw skips the persona hook, so active_persona here is the raw key —
	// which on an install that never ran `persona use` is only the shipped
	// default. Resolve it the way the loader does, or this reports no overlay
	// for the persona a new conversation would start with.
	id := strings.TrimSpace(persona.ResolveActiveID(cfg))
	if id == "" {
		return nil
	}
	profile, err := persona.Load(id)
	if err != nil {
		return nil
	}
	overrides := persona.OverriddenKeys(cfg, profile)
	if len(overrides) == 0 {
		return nil
	}
	name := strings.TrimSpace(profile.DisplayName)
	if name == "" {
		name = profile.ID
	}
	return &configPersonaPayload{ID: profile.ID, DisplayName: name, Overrides: overrides}
}

// overlayOverrides reports whether an already-resolved overlay claims one key.
func overlayOverrides(overlay *configPersonaPayload, key string) bool {
	if overlay == nil {
		return false
	}
	for _, overridden := range overlay.Overrides {
		if overridden == key {
			return true
		}
	}
	return false
}
