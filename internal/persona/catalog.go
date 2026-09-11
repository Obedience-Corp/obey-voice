package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Catalog provides instance-scoped profile resolution. An explicit directory
// lets an embedded host or proof fixture avoid the operator's configuration.
// The zero value reads the application's existing persona directory.
type Catalog struct{ Dir string }

func (c Catalog) root() string {
	if c.Dir != "" {
		return c.Dir
	}
	return Dir()
}

func (c Catalog) Load(id string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return LoadFile(filepath.Join(c.root(), id, "persona.yaml"))
}

func (c Catalog) List() ([]*Profile, error) {
	entries, err := os.ReadDir(c.root())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing personas: %w", err)
	}
	var profiles []*Profile
	for _, entry := range entries {
		if !entry.IsDir() || ValidateID(entry.Name()) != nil {
			continue
		}
		p, err := c.Load(entry.Name())
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// ResolveBinding always overlays a profile on the supplied base settings.
// It performs no migration, preference write, or asset installation.
func (c Catalog) ResolveBinding(base *config.Config, id string) (*SessionBinding, error) {
	if base == nil {
		return nil, fmt.Errorf("persona: config is nil")
	}
	if strings.TrimSpace(id) == "" {
		id = ActiveID(base)
	}
	p, err := c.Load(id)
	if err != nil {
		return nil, fmt.Errorf("resolve persona %q: %w", id, err)
	}
	snap := base.Clone()
	profiles, err := c.List()
	if err != nil {
		return nil, err
	}
	snap.PersonaDisplayNames = displayNames(profiles)
	Apply(snap, p)
	return newBinding(p.ID, p.DisplayName, p.TTS, *snap), nil
}
