package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
)

// ConfigFile returns the config file path.
func ConfigFile() string {
	return configFile
}

// ModelsDir returns the model cache directory.
func ModelsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	return v.GetString("models_dir")
}

// ModelsDirFrom resolves the model cache for an already-loaded Config without
// consulting mutable global viper state. Tests and TUI setup commands use this
// so an explicit models_dir remains isolated.
func ModelsDirFrom(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.ModelsDir) != "" {
		return strings.TrimSpace(cfg.ModelsDir)
	}
	return ModelsDir()
}

// SessionsDir returns the sessions directory.
func SessionsDir() string {
	return filepath.Join(configDir, "sessions")
}

// CapturesDir returns the TUI conversation-capture directory.
func CapturesDir() string {
	return filepath.Join(configDir, "captures")
}

// MeetingsDir returns the meeting transcripts directory.
// Honors meeting.dir when set; otherwise <configDir>/meetings.
func MeetingsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := strings.TrimSpace(v.GetString("meeting.dir")); d != "" {
		return expandHomePath(d)
	}
	return filepath.Join(configDir, "meetings")
}

// MeetingsDirFrom returns the meetings directory for an already-loaded Config
// without reading the global viper state (useful for tests and one-shot routes).
func MeetingsDirFrom(cfg *Config) string {
	if cfg != nil {
		if d := strings.TrimSpace(cfg.Meeting.Dir); d != "" {
			return expandHomePath(d)
		}
	}
	return filepath.Join(configDir, "meetings")
}

func expandHomePath(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

// ConfigDir returns the config directory path.
func ConfigDir() string {
	return configDir
}

// SetConfigDirForTest redirects ConfigDir (and the default config file path)
// for the duration of t. Used by persona and other packages that write under
// the install root.
//
// It also swaps in a fresh viper instance. Values written by SetAndSave live
// as in-memory overrides that outlive a config file, so without this a test
// that saves active_persona would carry that id into every later test in the
// package and fail it against a different temp install.
func SetConfigDirForTest(t interface {
	Helper()
	Cleanup(func())
}, dir string) {
	t.Helper()
	mu.Lock()
	origDir, origFile, origViper := configDir, configFile, v
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	v = viper.New()
	setDefaults(v)
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		configDir, configFile, v = origDir, origFile, origViper
	})
}

// PersonasDir returns <configDir>/personas.
func PersonasDir() string {
	return filepath.Join(configDir, "personas")
}

// PromptsDir returns the user prompt-documents directory: the configured
// prompts_dir when set, otherwise <config_dir>/prompts.
func PromptsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := v.GetString("prompts_dir"); d != "" {
		return d
	}
	return filepath.Join(configDir, "prompts")
}

// SkillsDir returns the Agent Skills directory: the configured skills_dir when
// set, otherwise <config_dir>/skills.
func SkillsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := v.GetString("skills_dir"); d != "" {
		return d
	}
	return filepath.Join(configDir, "skills")
}

// homeDir reports the home the install root and "~" paths derive from. A
// variable so a test can drive the branch where the OS has none to report.
// A test binary never gets the HOME it started with, so a test that skips
// SetConfigDirForTest cannot touch the real install; a HOME the test sets
// itself is honored.
var homeDir = func() string {
	home := osHomeDir()
	if testing.Testing() && home == inheritedHome {
		return testHomeDir()
	}
	return home
}

// With no OS home this and osHomeDir are both ".", so a test binary still redirects.
var inheritedHome = osHomeDir()

func osHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// testHomeDir is this test binary's home. The homes sit under one folder that
// only this user can enter; each binary locks its home for as long as it runs,
// and a later binary removes the homes whose lock is free. When that folder
// is someone else's, the home is a private one that nothing removes.
var testHomeDir = sync.OnceValue(func() string {
	home, lock := newTestHome(os.TempDir(), ownedPrivateDir)
	testHomeLock = lock
	return home
})

// testHomeLock stays open until the binary exits: closing it frees the lock.
var testHomeLock *os.File

func newTestHome(tmp string, private func(os.FileInfo) bool) (string, *os.File) {
	var b [8]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:]))
	parent := filepath.Join(tmp, testHomesParentName())
	if !claimTestHomesParent(parent, private) {
		if dir, err := os.MkdirTemp(tmp, AppSlug+"-test-home-"); err == nil {
			return dir, nil
		}
		return filepath.Join(tmp, AppSlug+"-test-home-"+name), nil
	}
	pruneTestHomes(parent)
	return filepath.Join(parent, name), lockTestHome(parent, name)
}

// testHomesParentName carries the uid because a shared temp directory holds
// every user's folder. Windows reports no uid and keeps a temp folder per user.
func testHomesParentName() string {
	if uid := os.Getuid(); uid >= 0 {
		return fmt.Sprintf("%s-test-homes-%d", AppSlug, uid)
	}
	return AppSlug + "-test-homes"
}

// claimTestHomesParent creates parent for this user alone, or accepts one that
// already is. Another user can create the name first in a shared temp
// directory, as a folder or a link, so an existing one is checked, not trusted.
func claimTestHomesParent(parent string, private func(os.FileInfo) bool) bool {
	if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return false
	}
	info, err := os.Lstat(parent)
	return err == nil && info.IsDir() && private(info)
}

// PromptsDirFrom resolves the user prompt-documents directory for an
// already-loaded Config without consulting mutable global viper state.
//
// Same contract as ModelsDirFrom, and the same reason: a library consumer holds
// its own *Config and must not have its paths silently answered by whatever the
// process last loaded. Falls back to the global lookup when the value is unset,
// so the CLI's precedence is unchanged.
func PromptsDirFrom(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.PromptsDir) != "" {
		return strings.TrimSpace(cfg.PromptsDir)
	}
	return PromptsDir()
}
