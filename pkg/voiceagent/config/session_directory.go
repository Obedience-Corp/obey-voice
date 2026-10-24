package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ValidateStartingDirectory normalizes a user choice without changing cwd or
// creating anything. Empty means inherit. Saved profiles remain loadable when
// a folder disappears; validation belongs to edits and conversation starts.
func ValidateStartingDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	dir, err := existingDir(value)
	if err != nil {
		return "", fmt.Errorf("starting directory %q: %w", value, err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("starting directory %q: %w", value, err)
	}
	if dir == rootDir {
		return "", fmt.Errorf("starting directory %q: choose a project folder, not the filesystem root", value)
	}
	// Opening and reading the directory checks readability. Stat of a child
	// checks search access without requiring write permission.
	f, err := os.Open(dir)
	if err != nil {
		return "", fmt.Errorf("starting directory %q: %w", value, err)
	}
	_, readErr := f.Readdirnames(1)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("starting directory %q: %w", value, readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("starting directory %q: %w", value, closeErr)
	}
	if _, err := os.Stat(dir + string(filepath.Separator) + "."); err != nil {
		return "", fmt.Errorf("starting directory %q: %w", value, err)
	}
	return dir, nil
}

// ResolveSessionDirectory resolves once, before constructing conversation
// resources. A saved directory always wins for its owning persona.
func ResolveSessionDirectory(cfg *Config, override, personaDir, savedDir, savedSource string) (WorkDirInfo, error) {
	if savedDir != "" && !filepath.IsAbs(savedDir) {
		return WorkDirInfo{}, fmt.Errorf("saved conversation directory %q must be absolute", savedDir)
	}
	candidate, source := savedDir, savedSource
	if candidate != "" && source == "" {
		source = "session"
	}
	if candidate == "" {
		candidate, source = strings.TrimSpace(override), "flag"
	}
	if candidate == "" {
		for _, key := range []string{"OBEY_VOICE_WORK_DIR", "WORK_DIR"} {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				candidate, source = value, "env"
				break
			}
		}
	}
	if candidate == "" {
		candidate, source = strings.TrimSpace(personaDir), "persona"
		if candidate != "" && !filepath.IsAbs(expandHomePath(candidate)) {
			return WorkDirInfo{}, fmt.Errorf("persona starting directory %q must be absolute; save it through persona edit", candidate)
		}
	}
	if candidate == "" {
		info := WorkDirInfoFrom(cfg)
		candidate, source = info.Dir, string(info.Source)
	}
	dir, err := ValidateStartingDirectory(candidate)
	if err != nil {
		if savedDir != "" {
			return WorkDirInfo{}, fmt.Errorf("saved conversation directory %q: %w; restore this folder or start a new conversation", savedDir, err)
		}
		return WorkDirInfo{}, fmt.Errorf("%s directory: %w; select an existing directory or clear the persona starting directory", source, err)
	}
	if dir == "" {
		return WorkDirInfo{}, fmt.Errorf("conversation directory is empty")
	}
	return WorkDirInfo{Dir: dir, Source: WorkDirSource(source)}, nil
}
