package persona

import (
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func init() {
	config.SetAfterLoad(EnsureAndApply)
}

// EnsureAndApply migrates a single-agent install when needed, then overlays
// the active persona profile onto cfg. Safe to call on every Load.
func EnsureAndApply(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("persona: config is nil")
	}

	if err := ensureMigrated(cfg); err != nil {
		return err
	}

	id := ActiveID(cfg)
	if err := ValidateID(id); err != nil {
		return fmt.Errorf("active_persona: %w", err)
	}

	p, err := Load(id)
	if err != nil {
		if os.IsNotExist(err) || isNotExist(err) {
			available, _ := List()
			var ids []string
			for _, x := range available {
				ids = append(ids, x.ID)
			}
			if len(ids) == 0 {
				return fmt.Errorf("active_persona %q not found (no personas under %s)", id, Dir())
			}
			return fmt.Errorf("active_persona %q not found (available: %s)", id, strings.Join(ids, ", "))
		}
		return err
	}

	Apply(cfg, p)
	return nil
}

func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	// LoadFile wraps os.ErrNotExist.
	return os.IsNotExist(err) || strings.Contains(err.Error(), "no such file")
}

// ensureMigrated populates the personas directory when it has no valid
// profiles yet, and resolves active_persona against what is actually on disk.
//
// Two things this code cannot read off a Config drive the whole function.
//
// Age: viper writes nothing and the config writer is surgical, so a missing
// persona key proves nothing about how old an install is. Only the artifacts
// do. No profiles AND no config file means nothing has ever run here — a fresh
// install, which gets the shipped built-ins. No profiles but a config file
// means an install that predates the personas directory: it migrates into the
// single profile its own keys describe, and nothing else is written.
//
// Ownership: "obey" is a name a user can give a persona, so the id alone says
// nothing about who wrote a profile. Adoption, healing and seeding all go
// through the builtin flag on disk instead (see resolveActive, pickFallbackID,
// SeedBuiltins), which is the record of what this code wrote.
//
// When profiles already exist, this call adds none.
func ensureMigrated(cfg *config.Config) error {
	existing, err := List()
	if err != nil {
		return err
	}

	if len(existing) == 0 {
		if config.FileExists() {
			// Pre-personas install: its identity lives in legacy config keys.
			seed := legacySeed(cfg)
			if err := Write(seed, true); err != nil {
				return fmt.Errorf("migrating default persona: %w", err)
			}
			// Always bind active to the seeded profile. seed.ID may be a legacy
			// persona slug (e.g. festival), not a built-in id.
			cfg.ActivePersona = seed.ID
			return nil
		}
		if err := SeedBuiltins(cfg); err != nil {
			return err
		}
		if existing, err = List(); err != nil {
			return err
		}
	}

	cfg.ActivePersona = resolveActive(cfg.ActivePersona, config.Declared("active_persona"), existing)
	return nil
}

// resolveActive picks the active persona id given the profiles on disk.
//
// chosen reports whether active_persona was named by this install — in the
// config file or a bound environment variable — rather than being the value
// viper filled in. That distinction is the whole point: an id the user chose is
// honored even when it lands on their own profile, while the shipped default is
// only allowed to land on a profile this code wrote. Without it, promoting
// "obey" to the default silently hands every install that already has a persona
// named obey a different agent, prompt, model and voice on first load.
//
// It writes nothing, so read-only surfaces can call it too (ResolveActiveID).
func resolveActive(id string, chosen bool, existing []*Profile) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id, chosen = DefaultID, false
	}
	if p := findProfile(existing, id); p != nil {
		if chosen || Owned(p) {
			return id
		}
		// A default that landed on someone else's profile is a name collision,
		// not a selection.
		return pickFallbackID(existing)
	}
	// The id names no profile here. A shipped id — or one nobody chose — heals
	// onto a real profile; an id the user chose is their error to see.
	if !chosen || IsBuiltinID(id) {
		return pickFallbackID(existing)
	}
	return id
}

// legacySeed builds the profile a pre-personas install implies, reading only
// the keys that install actually declared.
//
// An undeclared key is cleared before FromConfig sees it, so its legacy
// fallback applies: a config written before personas existed named no profile
// and no agent, and the identity it was running was Samantha. Leaving today's
// defaults in place would migrate that install into an "obey" profile wearing
// whatever name it had — the exact silent identity flip this path exists to
// prevent.
func legacySeed(cfg *config.Config) *Profile {
	legacy := *cfg
	if !config.Declared("persona") {
		legacy.Persona = ""
	}
	if !config.Declared("agent_name") {
		legacy.AgentName = ""
	}
	return FromConfig(&legacy)
}

func hasProfileID(profiles []*Profile, id string) bool {
	return findProfile(profiles, id) != nil
}

func findProfile(profiles []*Profile, id string) *Profile {
	for _, p := range profiles {
		if p != nil && p.ID == id {
			return p
		}
	}
	return nil
}

// pickFallbackID answers "which real profile should this install be on" when
// the configured id is unusable.
//
// Shipped profiles win, and only ones this code wrote count: a user persona
// named obey must never be adopted by an upgrade. The pre-rebrand default is
// preferred over anything else, including by id alone, because before the
// rebrand samantha WAS this function's answer and an install that never set
// active_persona must not be handed a different agent by an upgrade.
func pickFallbackID(profiles []*Profile) string {
	if len(profiles) == 0 {
		return DefaultID
	}
	for _, want := range builtinIDs {
		if p := findProfile(profiles, want); Owned(p) {
			return want
		}
	}
	if hasProfileID(profiles, LegacyDefaultID) {
		return LegacyDefaultID
	}
	return profiles[0].ID
}

// ResolveActiveID reports the persona id a config load settles on, including
// the heal ensureMigrated applies. It writes nothing, so read-only surfaces
// that load with LoadRaw (config get's persona overlay) can name the same
// persona the running agent uses instead of the raw key.
func ResolveActiveID(cfg *config.Config) string {
	id := ActiveID(cfg)
	existing, err := List()
	if err != nil || len(existing) == 0 {
		return id
	}
	return resolveActive(id, config.Declared("active_persona"), existing)
}

// Use sets active_persona, persists it, and applies the profile to cfg.
func Use(cfg *config.Config, id string) error {
	if cfg == nil {
		return fmt.Errorf("persona: config is nil")
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	p, err := Load(id)
	if err != nil {
		return err
	}
	if err := config.SetAndSave("active_persona", id); err != nil {
		return fmt.Errorf("saving active_persona: %w", err)
	}
	// Keep app config keys in sync so Settings / doctor / older tools match.
	_ = config.SetAndSave("agent_name", p.DisplayName)
	if ref := strings.TrimSpace(p.Prompts.Persona); ref != "" {
		_ = config.SetAndSave("persona", ref)
	}
	if err := PersistTTS(p); err != nil {
		return fmt.Errorf("saving persona tts: %w", err)
	}
	Apply(cfg, p)
	return nil
}

// UpdateStack persists brain + TTS choices onto one persona profile. This is
// the only write path for a persona's model/voice stack: Settings writes
// global defaults only (design WI-c8884d §5.2), and the Personas editor
// writes here. Empty fields clear back to inherit-global.
func UpdateStack(id string, brain Brain, tts TTS) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	p.Brain = Brain{
		Provider: strings.TrimSpace(brain.Provider),
		Model:    strings.TrimSpace(brain.Model),
	}
	p.TTS = TTS{
		Provider: strings.TrimSpace(tts.Provider),
		Voice:    strings.TrimSpace(tts.Voice),
		Tier:     canonicalTier(tts.Tier),
	}
	if err := Write(p, false); err != nil {
		return nil, fmt.Errorf("saving persona stack: %w", err)
	}
	return p, nil
}
