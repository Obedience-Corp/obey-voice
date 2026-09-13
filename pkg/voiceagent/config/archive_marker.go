package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func fileVerified(path string, expectedSize int64, sha256Hex string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return false
	}
	if sha256Hex != "" {
		got, err := fileSHA256(path)
		if err != nil || !strings.EqualFold(got, sha256Hex) {
			return false
		}
	}
	return true
}

type archiveInstallMarker struct {
	ID          string            `json:"id"`
	URL         string            `json:"url"`
	SHA256      string            `json:"sha256"`
	CheckHashes map[string]string `json:"check_hashes"`
	// Files are the top-level entry names promoted into the target dir at
	// extraction, relative to it. It records the archive's full on-disk footprint
	// (not just the check-file subset) so cleanup knows every path the asset owns.
	// Omitted by markers written before this field existed; readers must treat an
	// absent list conservatively.
	Files []string `json:"files,omitempty"`
}

func archiveInstallMarkerPath(dir, id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "archive"
	}
	return filepath.Join(dir, ".samantha-asset-"+name+".json")
}

func archiveInstallMarkerExists(dir, id string) bool {
	_, err := os.Stat(archiveInstallMarkerPath(dir, id))
	return err == nil
}

// archiveMarkerFiles returns the recorded top-level extraction footprint for an
// archive and whether a parseable marker exists. files is nil for legacy markers
// written before the footprint was recorded, so callers must distinguish "marker
// present but footprint unknown" (present=true, files=nil) from "no marker".
func archiveMarkerFiles(dir, id string) (files []string, present bool) {
	data, err := os.ReadFile(archiveInstallMarkerPath(dir, id))
	if err != nil {
		return nil, false
	}
	var marker archiveInstallMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, false
	}
	return marker.Files, true
}

func archiveInstallMarkerValid(dir, id, url, sha256Hex string, checkFiles []string) bool {
	data, err := os.ReadFile(archiveInstallMarkerPath(dir, id))
	if err != nil {
		return false
	}
	var marker archiveInstallMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	if marker.ID != id || marker.URL != url || !strings.EqualFold(marker.SHA256, sha256Hex) {
		return false
	}
	wantHashes, err := archiveCheckHashes(dir, checkFiles)
	if err != nil {
		return false
	}
	if len(marker.CheckHashes) != len(wantHashes) {
		return false
	}
	for path, got := range wantHashes {
		if !strings.EqualFold(got, marker.CheckHashes[path]) {
			return false
		}
	}
	return true
}

func writeArchiveInstallMarker(dir, id, url, sha256Hex string, checkFiles []string) error {
	return writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex, checkFiles, nil)
}

// writeArchiveInstallMarkerFiles is writeArchiveInstallMarker with the extracted
// top-level entry list recorded (see archiveInstallMarker.Files). files is nil
// when the footprint is unknown (e.g. adopting an already-extracted archive),
// which readers treat as a legacy marker.
func writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex string, checkFiles, files []string) error {
	hashes, err := archiveCheckHashes(dir, checkFiles)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(archiveInstallMarker{
		ID:          id,
		URL:         url,
		SHA256:      strings.ToLower(sha256Hex),
		CheckHashes: hashes,
		Files:       files,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := archiveInstallMarkerPath(dir, id)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func archiveCheckHashes(dir string, checkFiles []string) (map[string]string, error) {
	hashes := make(map[string]string, len(checkFiles))
	for _, rel := range checkFiles {
		sum, err := pathSHA256(filepath.Join(dir, rel))
		if err != nil {
			return nil, err
		}
		hashes[rel] = sum
	}
	return hashes, nil
}

func pathSHA256(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fileSHA256(path)
	}
	hasher := sha256.New()
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hasher.Write([]byte(rel))
		hasher.Write([]byte{0})
		if d.IsDir() {
			hasher.Write([]byte{1})
			return nil
		}
		hasher.Write([]byte{2})
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(hasher, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
