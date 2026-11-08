package config

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/internal/archiveutil"
)

// archiveExtracted checks if all expected files/dirs exist.
func archiveExtracted(dir string, checkFiles []string) bool {
	for _, f := range checkFiles {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

// extractTarStream extracts a decompressed tar stream into dir. It extracts into
// a temp directory inside dir, verifies that every check file is present, then
// promotes each top-level entry into dir, so dir is never left with a partial or
// rejected extraction. It returns the promoted top-level entry names (relative to
// dir) so callers can record the archive's footprint. It is the testable core
// (no bzip2/HTTP).
func extractTarStream(r io.Reader, dir, name string, checkFiles []string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	tmpDir, err := os.MkdirTemp(dir, ".extract-*")
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpDir)
		}
	}()

	if err := extractTar(tar.NewReader(r), tmpDir, name); err != nil {
		return nil, err
	}
	if !archiveExtracted(tmpDir, checkFiles) {
		return nil, fmt.Errorf("extract %s: archive missing expected files after extraction", name)
	}

	// Promote each top-level entry into the final directory, replacing any stale
	// partial from a previous run.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		src := filepath.Join(tmpDir, e.Name())
		dst := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(dst); err != nil {
			return nil, fmt.Errorf("extract %s: clear %s: %w", name, e.Name(), err)
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("extract %s: promote %s: %w", name, e.Name(), err)
		}
	}

	committed = true
	os.RemoveAll(tmpDir)
	return names, nil
}

// extractTar extracts every entry of tr into dir, stripping the archive's
// top-level directory prefix. It rejects unsafe paths (absolute or escaping the
// root) and link entries (symlinks/hardlinks), and ignores other special types.
func extractTar(tr *tar.Reader, dir, name string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	defer root.Close()
	var stripper archiveutil.Stripper
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("extract %s: reading archive: %w", name, err)
		}
		// Global PAX metadata is not a filesystem entry and must not decide
		// whether the following model files have a release-directory prefix.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}

		rel, err := stripper.Strip(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
		}

		switch header.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("extract %s: refusing link entry %q", name, rel)
		}

		if rel == "." {
			continue
		}
		target := filepath.FromSlash(rel)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("extract %s: create dir %s: %w", name, rel, err)
			}
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("extract %s: create parent dir for %s: %w", name, rel, err)
			}
			f, err := root.Create(target)
			if err != nil {
				return fmt.Errorf("extract %s: create file %s: %w", name, rel, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("extract %s: write file %s: %w", name, rel, err)
			}
			if err := f.Chmod(os.FileMode(header.Mode).Perm()); err != nil {
				f.Close()
				return fmt.Errorf("extract %s: chmod %s: %w", name, rel, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("extract %s: close %s: %w", name, rel, err)
			}
		}
	}
	return nil
}
