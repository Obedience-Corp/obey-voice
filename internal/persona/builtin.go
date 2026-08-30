package persona

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// IsBuiltinID reports whether id is one the product ships a profile under.
// Membership is about the NAME, not about ownership: a profile at one of these
// ids is ours only when its own builtin flag says we wrote it (see Owned).
func IsBuiltinID(id string) bool {
	id = strings.TrimSpace(id)
	for _, want := range ShippedIDs() {
		if id == want {
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

// Builtins returns the shipped profiles, default first. The catalog is the
// YAML under shipped/; this function does not special-case companions.
//
// obey pins nothing — no brain, no TTS — so the default persona speaks with
// whatever the app config says. Display names come from the YAML, not from
// whatever agent_name happens to be at seed time (that would rename Obey
// to the currently active companion when filling a missing slot).
func Builtins(cfg *config.Config) []*Profile {
	_ = cfg
	return shippedProfiles()
}

// SeedBuiltins writes any shipped profile whose slot is still free.
//
// Call it on every load, not just a first run: that is how a new companion
// (Veronica today, whoever next) appears on an install that already has
// personas. Occupied slots stay occupied. A user persona named obey or
// veronica keeps its file, its pins and its deletability.
func SeedBuiltins(cfg *config.Config) error {
	for _, p := range Builtins(cfg) {
		if err := Write(p, true); err != nil {
			return fmt.Errorf("seeding built-in persona %q: %w", p.ID, err)
		}
	}
	return nil
}
