package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

var (
	configDir  = DefaultConfigDir()
	configFile = filepath.Join(configDir, "config.yaml")
	v          *viper.Viper
	// mu guards v: the TUI reads config from tea.Cmd goroutines while the
	// Update goroutine writes via SetAndSave.
	//
	// mu does NOT guard the fields of a *Config value returned by Load — that
	// struct is plain data with no internal synchronization. A *Config must not
	// be shared by pointer between a goroutine that mutates its fields (e.g. the
	// TUI Update loop) and one that reads them live (a pipeline/brain turn).
	// Hand concurrent readers an independent snapshot instead: a fresh Load, or a
	// value copy (next := *cfg) as serve/meeting/benchmark do before overriding a
	// field. The TUI honors this via conversationRuntimeBuilder's per-conversation
	// Load; see the App.cfg invariant in internal/tui/tui.go.
	mu sync.RWMutex
)

func init() {
	v = viper.New()
	setDefaults(v)
}

// Get returns a config value by key.
func Get(key string) any {
	mu.RLock()
	defer mu.RUnlock()
	return v.Get(key)
}

// Declared reports whether key's value was chosen — named in the config file
// or in a bound environment variable — rather than coming from the shipped
// default. viper merges all three into one value, so a caller that must tell a
// decision from a default (persona migration does) cannot read Config alone.
//
// It answers for the file the last load read. ollamaChosen makes the same
// distinction inline for brain_provider.
func Declared(key string) bool {
	mu.RLock()
	inFile := v.InConfig(key)
	mu.RUnlock()
	if inFile {
		return true
	}
	_, _, fromEnv := envValueFor(key)
	return fromEnv
}

// FileExists reports whether a config file is present at ConfigFile().
//
// It is the one signal about an install's age that neither viper nor the
// environment can fabricate: keys are written surgically, so an absent key
// proves nothing, but an absent file proves nothing has ever configured this
// install.
func FileExists() bool {
	mu.RLock()
	path := configFile
	mu.RUnlock()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Set updates a config value.
func Set(key string, value any) {
	mu.Lock()
	defer mu.Unlock()
	v.Set(key, value)
}

// Save persists current config to disk.
func Save() error {
	mu.Lock()
	defer mu.Unlock()
	return save()
}

// save writes the config file. Callers must hold mu.
func save() error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return v.WriteConfigAs(configFile)
}

// SetAndSave updates one already-typed value and persists it.
//
// It is a thin wrapper over SetKeyFile so the TUI writes through the same
// surgical writer as the CLI and the Mac app: only the changed key's line is
// rewritten, and comments and key order survive.
func SetAndSave(key string, value any) error {
	_, err := SetKeyFileValue(key, value)
	return err
}

// ValidateAndSet coerces raw by key's schema type and persists it, rejecting
// unknown keys so a typo can't corrupt the config file. The name is kept for
// the callers that already use it; the behaviour is SetKeyFile's.
func ValidateAndSet(key, raw string) error {
	_, err := SetKeyFile(key, raw)
	return err
}

// AllSettings returns all config as a map.
func AllSettings() map[string]any {
	mu.RLock()
	defer mu.RUnlock()
	return v.AllSettings()
}

// AllKeys returns all config keys.
func AllKeys() []string {
	mu.RLock()
	defer mu.RUnlock()
	return v.AllKeys()
}

// LanguageCode returns the lowercase language code of a locale tag
// ("en-US" -> "en"), falling back to "en" when empty.
func LanguageCode(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return "en"
	}
	parts := strings.Split(lang, "-")
	return strings.ToLower(parts[0])
}
