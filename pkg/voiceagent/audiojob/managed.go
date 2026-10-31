package audiojob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ManagedModel = "lfm3-audio-2b"
const ManagedVersion = "b11960-8642541b2"

// ManagedFile is supplied by the editor after verifying its complete pack
// inventory. Files are checked again here before executing or loading them.
type ManagedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type ManagedConfig struct {
	Executable ManagedFile `json:"executable"`
	Model      ManagedFile `json:"model"`
	Projector  ManagedFile `json:"projector"`
	Version    string      `json:"version"`
}
type Sampling struct {
	Seed           int     `json:"seed"`
	MaxAudioFrames int     `json:"max_audio_frames"`
	RepeatPenalty  float64 `json:"repeat_penalty"`
}

func (m ManagedConfig) Validate() error {
	if m.Version != ManagedVersion {
		return fmt.Errorf("managed audio requires reviewed runtime %s", ManagedVersion)
	}
	for _, file := range []ManagedFile{m.Executable, m.Model, m.Projector} {
		if !filepath.IsAbs(file.Path) || !HashValid(file.SHA256) {
			return fmt.Errorf("managed audio requires absolute paths and SHA-256 for every asset")
		}
	}
	return nil
}

func (m ManagedConfig) verify(ctx context.Context) error {
	if err := m.Validate(); err != nil {
		return err
	}
	for i, file := range []ManagedFile{m.Executable, m.Model, m.Projector} {
		info, err := os.Lstat(file.Path)
		if err != nil {
			return fmt.Errorf("managed asset: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || (i == 0 && info.Mode().Perm()&0111 == 0) {
			return fmt.Errorf("managed asset must be a nonempty regular file (runtime must be executable): %s", file.Path)
		}
		hash, err := fileHash(ctx, file.Path)
		if err != nil {
			return err
		}
		if hash != file.SHA256 {
			return fmt.Errorf("managed asset hash changed: %s; repair the installation", file.Path)
		}
	}
	return nil
}

func managedEnvironment(scratch string) []string {
	// No inherited LLAMA_*, proxy, auth, HF cache, or assistant configuration.
	return []string{"HOME=" + scratch, "TMPDIR=" + scratch, "PATH=/usr/bin:/bin", "LANG=C", "HF_HUB_OFFLINE=1", "NO_COLOR=1"}
}

func verifiedVersion(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "version: 0.8.0-dev (build 11960, commit 8642541b2)" {
			return true
		}
	}
	return false
}
