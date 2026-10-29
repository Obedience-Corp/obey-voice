package persona

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// SessionBinding is the identity snapshot a conversation session runs with:
// persona id, display/agent name, system prompt reference, and voice. It is
// resolved at a response boundary. Later profile edits or global preferences
// cannot mutate an admitted response; the next boundary resolves a fresh copy.
type SessionBinding struct {
	PersonaID   string
	DisplayName string
	// AgentName is the effective assistant name after applying the profile
	// over the config (profiles with an empty display name keep the default).
	AgentName string
	// PromptRef is the system prompt doc name the brain resolves at build.
	PromptRef string
	// BrainProvider / BrainModel are the effective routing after applying the
	// profile over the config (empty profile fields inherit the app default).
	BrainProvider string
	BrainModel    string
	TTS           TTS

	cfg config.Config
}

// ResolveBinding loads the persona profile (empty id resolves the configured
// active persona) and returns the identity snapshot plus a binding-derived
// config. The snapshot is the only config a session runtime should hold:
// later persona.Use / Apply / Settings writes mutate other Config values,
// never a resolved binding.
func ResolveBinding(cfg *config.Config, id string) (*SessionBinding, error) {
	if cfg == nil {
		return nil, fmt.Errorf("persona: config is nil")
	}
	if strings.TrimSpace(id) == "" {
		id = ActiveID(cfg)
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		// A raw "no such file" sends the user looking for a path bug. Name the
		// personas that do exist instead — same shape as EnsureAndApply's
		// message, so both entry points fail the same way.
		if isNotExist(err) {
			if available, listErr := List(); listErr == nil && len(available) > 0 {
				ids := make([]string, 0, len(available))
				for _, x := range available {
					ids = append(ids, x.ID)
				}
				return nil, fmt.Errorf("persona %q not found (available: %s)", id, strings.Join(ids, ", "))
			}
		}
		return nil, err
	}
	if err := RequireEnabled(p); err != nil {
		return nil, err
	}
	return bindProfile(cfg, p)
}

func bindProfile(cfg *config.Config, p *Profile) (*SessionBinding, error) {
	snap := *cfg
	fillDisplayNames(&snap)
	Apply(&snap, p)
	if err := config.BindPersonaDirectory(&snap, p.ID, p.StartingDirectory); err != nil {
		return nil, err
	}
	return newBinding(p.ID, p.DisplayName, p.TTS, snap), nil
}

// ResolveDefaultsBinding starts the complete saved Obey persona. Settings fill
// fields that the profile inherits, exactly as for any explicitly named persona.
// The embedded profile covers a fresh install whose files have not been seeded.
func ResolveDefaultsBinding(cfg *config.Config) (*SessionBinding, error) {
	if cfg == nil {
		return nil, fmt.Errorf("persona: config is nil")
	}
	p, err := Load(DefaultID)
	if err != nil {
		if !isNotExist(err) {
			return nil, err
		}
		for _, shipped := range Builtins(cfg) {
			if shipped.ID == DefaultID {
				p = shipped
				break
			}
		}
		if p == nil {
			return nil, fmt.Errorf("default persona %q is unavailable", DefaultID)
		}
	}
	if err := RequireEnabled(p); err != nil {
		return nil, err
	}
	return bindProfile(cfg, p)
}

func newBinding(id, display string, tts TTS, snap config.Config) *SessionBinding {
	return &SessionBinding{
		PersonaID:     id,
		DisplayName:   display,
		AgentName:     snap.AgentName,
		PromptRef:     snap.Persona,
		BrainProvider: snap.BrainProvider,
		BrainModel:    modelForProvider(&snap, snap.BrainProvider),
		TTS:           tts,
		cfg:           snap,
	}
}

// Config returns a fresh copy of the binding-derived config, so no caller can
// mutate the snapshot the session was bound to.
func (b *SessionBinding) Config() *config.Config {
	return b.cfg.Clone()
}
