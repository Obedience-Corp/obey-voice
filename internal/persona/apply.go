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

// EnsureAndApply seeds any missing shipped personas, then overlays the active
// persona profile onto cfg. Safe to call on every Load.
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

// ensureMigrated fills any missing shipped-persona slots and resolves
// active_persona against what is actually on disk.
//
// Seeding runs every load, skip-existing. That is how a new companion lands
// on an install that already has personas, without rewriting a slot the user
// already occupies. Ownership is the builtin flag on disk, not the id:
// "obey" and "veronica" are names a user can pick.
func ensureMigrated(cfg *config.Config) error {
	if err := SeedBuiltins(cfg); err != nil {
		return err
	}
	existing, err := List()
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return fmt.Errorf("persona: seeding shipped profiles produced none under %s", Dir())
	}

	cfg.ActivePersona = resolveActive(cfg.ActivePersona, config.Declared("active_persona"), existing)
	cfg.PersonaDisplayNames = displayNames(existing)
	return nil
}

// displayNames lists the display names of profiles, in catalog order.
func displayNames(profiles []*Profile) []string {
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		if p == nil || strings.TrimSpace(p.DisplayName) == "" {
			continue
		}
		names = append(names, p.DisplayName)
	}
	return names
}

// fillDisplayNames gives a snapshot the persona names it lacks. A config
// loaded without the after-load hook (LoadRaw) has none.
func fillDisplayNames(cfg *config.Config) {
	if len(cfg.PersonaDisplayNames) > 0 {
		return
	}
	if all, err := List(); err == nil {
		cfg.PersonaDisplayNames = displayNames(all)
	}
}

// resolveActive picks the active persona id given the profiles on disk.
//
// chosen reports whether active_persona was named by this install — in the
// config file or a bound environment variable — rather than being the value
// viper filled in. An id the user chose is honored even when it lands on
// their own profile. The shipped default is only allowed to land on a
// profile this code wrote.
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
// Shipped profiles this code wrote win, default first. A user persona that
// happens to be named obey is not adopted. There is no second-class "legacy
// default": samantha is a companion in the catalog like veronica.
func pickFallbackID(profiles []*Profile) string {
	if len(profiles) == 0 {
		return DefaultID
	}
	for _, want := range ShippedIDs() {
		if p := findProfile(profiles, want); Owned(p) {
			return want
		}
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
