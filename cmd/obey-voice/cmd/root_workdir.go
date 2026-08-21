//go:build !integration

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// resolvedWorkDir is the agent working directory this process resolved at
// startup. Written once in PersistentPreRunE, before any command starts a
// goroutine, and read-only afterwards.
var resolvedWorkDir string

// stampWorkDir pins the process-resolved working directory onto a freshly
// loaded config, so every brain the CLI builds grounds itself in the same
// folder no matter what the cwd is by the time it is built.
func stampWorkDir(cfg *config.Config) {
	if cfg != nil && resolvedWorkDir != "" {
		cfg.WorkDir = resolvedWorkDir
	}
}

// absPathFlag pins a relative path flag to the caller's cwd. Every such flag
// must go through this before enterWorkDir, or a path the user typed against
// their shell's directory would be opened against the agent's instead.
func absPathFlag(path *string) {
	if *path == "" || filepath.IsAbs(*path) {
		return
	}
	if abs, err := filepath.Abs(*path); err == nil {
		*path = abs
	}
}

// enterWorkDir moves the process into the agent's working directory, so the
// tools, skills roots and camp calls of every provider start there.
func enterWorkDir(dir string) error {
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("enter work dir %s: %w", dir, err)
	}
	return nil
}

// serveBannerWorkDir is the directory serve chdir'd into. The fallback keeps a
// banner emitted outside serve's own run honest: it is the same resolver, so
// the field is never an empty string on the wire.
func serveBannerWorkDir() string {
	if serveWorkDirResolved != "" {
		return serveWorkDirResolved
	}
	return config.WorkDir()
}
