package persona

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Catalog provides instance-scoped profile resolution. An explicit directory
// lets an embedded host or proof fixture avoid the operator's configuration.
// The zero value reads the application's existing persona directory.
type Catalog struct{ Dir string }

// ProfileLoadError marks failure loading the explicitly requested profile.
// Unrelated malformed entries are returned as CatalogIssue values instead.
type ProfileLoadError struct {
	ID  string
	Err error
}

func (e *ProfileLoadError) Error() string {
	return fmt.Sprintf("persona %q is unavailable until repaired: %v", e.ID, e.Err)
}
func (e *ProfileLoadError) Unwrap() error { return e.Err }

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
	profiles, _, err := c.ListWithIssues()
	return profiles, err
}

// ListWithIssues keeps healthy profiles available when another profile is
// malformed. The issue list lets a session surface a repair warning without
// turning an unrelated edit into a routing failure.
func (c Catalog) ListWithIssues() ([]*Profile, []CatalogIssue, error) {
	return listProfilesWithIssues(c.root(), c.Load)
}

// ResolveBinding always overlays a profile on the supplied base settings.
// It performs no migration, preference write, or asset installation.
func (c Catalog) ResolveBinding(base *config.Config, id string) (*SessionBinding, error) {
	binding, _, err := c.ResolveBindingWithIssues(base, id)
	return binding, err
}

// ResolveBindingWithIssues resolves the requested profile and returns any
// unrelated unloadable profiles discovered while assembling display names.
// The requested profile itself remains a hard failure.
func (c Catalog) ResolveBindingWithIssues(base *config.Config, id string) (*SessionBinding, []CatalogIssue, error) {
	if base == nil {
		return nil, nil, fmt.Errorf("persona: config is nil")
	}
	if strings.TrimSpace(id) == "" {
		id = ActiveID(base)
	}
	p, err := c.Load(id)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve persona %q: %w", id, &ProfileLoadError{ID: id, Err: err})
	}
	snap := base.Clone()
	profiles, issues, err := c.ListWithIssues()
	if err != nil {
		return nil, issues, err
	}
	snap.PersonaDisplayNames = displayNames(profiles)
	Apply(snap, p)
	return newBinding(p.ID, p.DisplayName, p.TTS, *snap), issues, nil
}
