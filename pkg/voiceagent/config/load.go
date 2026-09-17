package config

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
)

// afterLoad is optional post-processing (persona ensure/apply). Set via
// SetAfterLoad; persona package registers itself from init.
var afterLoad func(*Config) error

// SetAfterLoad registers a hook invoked at the end of Load (after unlock).
// Used by the persona package to migrate/apply active persona profiles.
//
// This is a process-wide side effect of loading config, and it is load-bearing
// for the CLI: persona application happens here and nowhere else. A library
// consumer that never wants a persona overlaid on its config should call
// LoadWithout instead of trying to unregister the hook — unregistering would
// change behaviour for every other caller in the process.
func SetAfterLoad(fn func(*Config) error) {
	afterLoad = fn
}

// LoadWithoutHooks reads configuration exactly as Load does but skips the
// after-load hook.
//
// It exists for embedders. The hook is registered from an init function in the
// persona package, so merely importing something that transitively imports
// persona opts a host into persona application it may not want. Load remains the
// CLI's entry point and its behaviour is unchanged.
func LoadWithoutHooks() (*Config, error) {
	return loadLocked()
}

// Load reads configuration from disk and environment.
func Load() (*Config, error) {
	cfg, err := loadLocked()
	if err != nil {
		return nil, err
	}
	if afterLoad != nil {
		if err := afterLoad(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// LoadRaw reads configuration from disk and environment without the
// afterLoad persona overlay, so it never writes migration files and cannot
// fail on persona state. Read-only discovery (`obey-voice paths`) uses it to
// resolve effective directories on installs Load would refuse or mutate.
func LoadRaw() (*Config, error) {
	return loadLocked()
}

// loadLocked reads and unmarshals config while holding mu.
func loadLocked() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()

	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	for key := range envBindings {
		// Multiple names per key: viper takes the first one that is set, which
		// is what makes the documented binding outrank a compatibility alias.
		_ = v.BindEnv(append([]string{key}, envNamesFor(key)...)...)
	}

	// Read config file (missing file is OK — use defaults)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("reading config: %w", err)
			}
		}
	}
	if raw, err := os.ReadFile(configFile); err == nil {
		if doc, parseErr := migrationYAMLDocument(raw); parseErr == nil {
			if node := mappingValue(doc.Content[0], "voice_workers"); node != nil {
				if err := validateWorkerYAML(node); err != nil {
					return nil, fmt.Errorf("parsing voice_workers: %w", err)
				}
			}
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.VoiceWorkers != nil {
		if err := cfg.VoiceWorkers.Validate(); err != nil {
			return nil, fmt.Errorf("parsing voice_workers: %w", err)
		}
	}
	applyOllamaDefaults(&cfg, v, ollamaChosen(v))
	cfg.ToolCommandTimeout = ClampToolCommandTimeout(cfg.ToolCommandTimeout)
	cfg.OllamaNumBatch = ClampOllamaNumBatch(cfg.OllamaNumBatch, cfg.OllamaNumCtx)
	resolveVoiceFrontendMigration(v, &cfg)
	resolveBargeInVADThresholdMigration(v, &cfg)
	return &cfg, nil
}
