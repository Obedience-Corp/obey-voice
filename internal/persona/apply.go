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
// profiles yet, and heals active_persona when it names a profile that is not
// there.
//
// active_persona defaults to DefaultID via viper, so an empty/unset value is
// indistinguishable from that default. Two seeding paths follow from that:
//
//   - A config that names a persona other than the default describes an
//     install that was running something before this one: it migrates into the
//     single profile its keys describe (legacySeed), and nothing else is
//     written — the profile list such an install wakes up with is the one it
//     had.
//   - A config that names only defaults is a fresh install and gets the
//     shipped built-ins: obey, which is the default, plus samantha.
//
// When profiles already exist, this call adds none. An install that was
// talking to samantha before the rebrand keeps talking to samantha, whether
// active_persona says so explicitly or fell through to the default.
func ensureMigrated(cfg *config.Config) error {
	existing, err := List()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		id := strings.TrimSpace(cfg.ActivePersona)
		if id == "" {
			id = DefaultID
		}
		if hasProfileID(existing, id) {
			cfg.ActivePersona = id
			return nil
		}
		// Heal: active_persona names a built-in that is not on this install —
		// either viper defaulted it to obey while only pre-rebrand profiles
		// exist, or the reverse, a config that still says samantha on an
		// install whose profile she never got. Neither is worth a hard error;
		// an unknown id the user chose is.
		if IsBuiltinID(id) {
			cfg.ActivePersona = pickFallbackID(existing)
			return nil
		}
		// Explicit unknown id — leave for Load to error with a clear message.
		cfg.ActivePersona = id
		return nil
	}

	if id := legacyPersonaID(cfg); id != "" {
		seed := legacySeed(cfg, id)
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
	cfg.ActivePersona = DefaultID
	return nil
}

// legacyPersonaID returns the persona this config was already running, or ""
// when it names nothing but the current defaults.
//
// A value equal to DefaultID cannot be told apart from the shipped default, so
// it never counts as a migration signal — otherwise a fresh install would take
// the migration path. persona wins over active_persona because that is the
// order the old migration read them in: a pre-personas config carries only
// persona, while active_persona may be a stale in-memory default.
func legacyPersonaID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, named := range []string{cfg.Persona, cfg.ActivePersona} {
		named = strings.TrimSpace(named)
		if named == "" || named == DefaultID {
			continue
		}
		if ValidateID(named) == nil {
			return named
		}
		// A prompt name that is not a profile id (the real Uncle_Fu case) still
		// says this install had an identity. It migrates under the pre-personas
		// id, keeping its prompt ref — which is what the old code did with it.
		return LegacyDefaultID
	}
	return ""
}

// legacySeed is FromConfig bound to the id the config actually names, so the
// migrated profile carries the install's own agent name, prompt and voice.
//
// The prompt ref is repointed only when it still holds the shipped default: an
// install whose active_persona says samantha while persona was never written
// must not migrate into a samantha profile that reads the obey identity.
func legacySeed(cfg *config.Config, id string) *Profile {
	p := FromConfig(cfg)
	p.ID = id
	p.Builtin = IsBuiltinID(id)
	if ref := strings.TrimSpace(p.Prompts.Persona); ref == "" || ref == DefaultID {
		p.Prompts.Persona = id
	}
	return p
}

func hasProfileID(profiles []*Profile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// pickFallbackID prefers the built-in default, then the pre-rebrand default,
// then the first listed profile. The middle step is what keeps an install that
// predates obey on samantha instead of on whatever sorts first: before the
// rebrand samantha WAS this function's answer, and an install that never set
// active_persona must not be handed a different agent by an upgrade.
func pickFallbackID(profiles []*Profile) string {
	if len(profiles) == 0 {
		return DefaultID
	}
	for _, want := range builtinIDs {
		for _, p := range profiles {
			if p.ID == want {
				return want
			}
		}
	}
	return profiles[0].ID
}

// ResolveActiveID reports the persona id a config load settles on, including
// the heal ensureMigrated applies when active_persona names a built-in this
// install does not have. It writes nothing, so read-only surfaces that load
// with LoadRaw (config get's persona overlay) can name the same persona the
// running agent uses instead of the raw key.
func ResolveActiveID(cfg *config.Config) string {
	id := ActiveID(cfg)
	if Exists(id) || !IsBuiltinID(id) {
		return id
	}
	existing, err := List()
	if err != nil || len(existing) == 0 {
		return id
	}
	return pickFallbackID(existing)
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
