package config

import (
	"os"
	"path/filepath"
	"strings"

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

// homeDir reports the user's home directory. A variable so a test can drive
// the branch where the OS has none to report.
var homeDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
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
