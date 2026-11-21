package prompts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// seedManifestName is the file in the prompts directory where Seed records
// the sha256 of the bytes it last wrote at each path. It is bookkeeping, not
// a prompt document.
const seedManifestName = ".seeded.json"

type seedManifest struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// loadSeedManifest never fails: a missing or unreadable manifest only means
// Seed recognises fewer files as its own, which leaves them alone.
func loadSeedManifest(dir string) *seedManifest {
	m := &seedManifest{Version: 1, Files: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(dir, seedManifestName))
	if err != nil {
		return m
	}
	var loaded seedManifest
	if json.Unmarshal(data, &loaded) != nil || loaded.Files == nil {
		return m
	}
	m.Files = loaded.Files
	return m
}

func (m *seedManifest) save(dir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding seed manifest: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, seedManifestName), append(data, '\n'))
}
