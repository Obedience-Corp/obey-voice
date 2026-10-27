package persona

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// Shipped personas live in shipped/*.yaml. Adding an internal persona is two
// files: this profile and pkg/voiceagent/prompts/defaults/persona.<id>.yaml.
// There is no Go special case per companion, and no "legacy default" besides
// DefaultID (obey), which is the fresh-install active persona.
//
//go:embed shipped/*.yaml
var shippedFS embed.FS

var (
	shippedOnce sync.Once
	shippedErr  error
	shippedAll  []*Profile
)

func shippedProfiles() []*Profile {
	shippedOnce.Do(func() {
		entries, err := fs.ReadDir(shippedFS, "shipped")
		if err != nil {
			shippedErr = fmt.Errorf("persona: read shipped catalog: %w", err)
			return
		}
		var list []*Profile
		seen := map[string]string{}
		for _, entry := range entries {
			if entry.IsDir() || path.Ext(entry.Name()) != ".yaml" {
				continue
			}
			name := entry.Name()
			data, err := shippedFS.ReadFile("shipped/" + name)
			if err != nil {
				shippedErr = fmt.Errorf("persona: read shipped %s: %w", name, err)
				return
			}
			p, err := decodeShipped(data)
			if err != nil {
				shippedErr = fmt.Errorf("persona: shipped %s: %w", name, err)
				return
			}
			if prev, ok := seen[p.ID]; ok {
				shippedErr = fmt.Errorf("persona: duplicate shipped id %q (%s and %s)", p.ID, prev, name)
				return
			}
			seen[p.ID] = name
			list = append(list, p)
		}
		if _, ok := seen[DefaultID]; !ok {
			shippedErr = fmt.Errorf("persona: shipped catalog missing default id %q", DefaultID)
			return
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].ID == DefaultID {
				return true
			}
			if list[j].ID == DefaultID {
				return false
			}
			return list[i].ID < list[j].ID
		})
		shippedAll = list
	})
	if shippedErr != nil {
		panic(shippedErr)
	}
	out := make([]*Profile, len(shippedAll))
	for i, p := range shippedAll {
		cp := *p
		cp.Aliases = slices.Clone(p.Aliases)
		out[i] = &cp
	}
	return out
}

func decodeShipped(data []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !p.Builtin {
		return nil, fmt.Errorf("%s: shipped profiles must set builtin: true", p.ID)
	}
	if p.Prompts.Persona != p.ID {
		return nil, fmt.Errorf("%s: prompts.persona must be the profile id", p.ID)
	}
	return &p, nil
}

// ShippedIDs returns every catalog id, default first, then alphabetical.
func ShippedIDs() []string {
	list := shippedProfiles()
	ids := make([]string, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	return ids
}
