package phonon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	MultilingualModel    = "gradium/phonon-40m-multilingual"
	multilingualRevision = "1537ce46c64f42903e0d608b132ccb073dac46d2"
	// MultilingualVoiceCount excludes the checkpoint's own default voice.
	MultilingualVoiceCount = 102
)

func (o Options) ModelID() string {
	if o.Multilingual {
		return MultilingualModel
	}
	return Model
}

func (o Options) Revision() string {
	if o.Multilingual {
		return multilingualRevision
	}
	return ModelRevision
}

func (o Options) readyFile() string {
	if o.Multilingual {
		return filepath.Join(o.installDir(), "ready-multilingual.json")
	}
	return filepath.Join(o.installDir(), "ready.json")
}

// The English installation predates model selection. Keep its readiness contract.
// For the full pack, verify every embedding listed in the pinned catalog.
func (o Options) requiredVoices() (names, files []string, err error) {
	if !o.Multilingual {
		return namedVoices, namedVoiceFiles, nil
	}
	path := filepath.Join(o.cache(), "hub", "models--gradium--phonon-voices", "snapshots", CatalogRevision, "voices.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var v struct{ Name, Filename string }
		if err := json.Unmarshal(scanner.Bytes(), &v); err != nil {
			return nil, nil, fmt.Errorf("read Phonon catalog: %w", err)
		}
		if v.Name == "" || seen[v.Name] || !filepath.IsLocal(v.Filename) {
			return nil, nil, fmt.Errorf("invalid Phonon catalog entry for %q", v.Name)
		}
		seen[v.Name] = true
		names = append(names, v.Name)
		files = append(files, v.Filename)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if len(names) != MultilingualVoiceCount {
		return nil, nil, fmt.Errorf("pinned Phonon catalog contains %d voices, expected %d", len(names), MultilingualVoiceCount)
	}
	return names, files, nil
}
