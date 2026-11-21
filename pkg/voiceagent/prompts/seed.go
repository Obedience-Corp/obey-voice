package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// Seed writes the embedded default documents into dir, one per kind at
// dir/<kind>/<name>.yaml, so a user can discover and edit a real starting
// file instead of authoring the schema from scratch. It is safe to call on
// every startup and returns the relative paths it wrote (empty when nothing
// changed).
//
// A file that is byte-for-byte a copy this product wrote earlier is refreshed
// to the current shipped version; anything else is the user's and is never
// touched. Earlier copies are recognised from a manifest Seed keeps in dir and,
// for installs seeded before that manifest existed, from shippedHistory.
func Seed(dir string) ([]string, error) {
	sources, err := seedSources()
	if err != nil {
		return nil, err
	}
	return seed(dir, sources, shippedHistory)
}

type seedSource struct {
	rel  string // slash-separated <kind>/<name>.yaml
	data []byte
}

func seedSources() ([]seedSource, error) {
	entries, err := fs.ReadDir(defaultsFS, "defaults")
	if err != nil {
		return nil, fmt.Errorf("reading embedded defaults: %w", err)
	}
	var sources []seedSource
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := defaultsFS.ReadFile("defaults/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading embedded default %s: %w", entry.Name(), err)
		}
		// Load to derive the on-disk path (kind/name) from the document itself;
		// the raw bytes are what we write, preserving comments and the schema
		// header.
		doc, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("embedded default %s: %w", entry.Name(), err)
		}
		sources = append(sources, seedSource{rel: path.Join(string(doc.Prompt.Kind), doc.Prompt.Name+".yaml"), data: data})
	}
	return sources, nil
}

func seed(dir string, sources []seedSource, history map[string][]string) (written []string, err error) {
	manifest := loadSeedManifest(dir)
	changed := false
	record := func(rel, sum string) {
		if manifest.Files[rel] != sum {
			manifest.Files[rel] = sum
			changed = true
		}
	}
	defer func() {
		if changed {
			err = errors.Join(err, manifest.save(dir))
		}
	}()

	for _, src := range sources {
		shipped := sha256Hex(src.data)
		dst := filepath.Join(dir, filepath.FromSlash(src.rel))

		info, statErr := os.Lstat(dst)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
		case statErr != nil:
			return written, fmt.Errorf("checking %s: %w", dst, statErr)
		case !info.Mode().IsRegular():
			continue
		default:
			current, readErr := os.ReadFile(dst)
			if readErr != nil {
				continue
			}
			sum := sha256Hex(current)
			if sum == shipped {
				record(src.rel, shipped)
				continue
			}
			if sum != manifest.Files[src.rel] && !slices.Contains(history[src.rel], sum) {
				continue
			}
		}

		if err := writeFileAtomic(dst, src.data); err != nil {
			return written, err
		}
		record(src.rel, shipped)
		written = append(written, filepath.FromSlash(src.rel))
	}
	return written, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic replaces dst through a temporary sibling so a concurrent
// reader never sees a partial document. The temporary name deliberately has
// no prompt extension, so a listing walk never mistakes it for a document.
func writeFileAtomic(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".seed-*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
