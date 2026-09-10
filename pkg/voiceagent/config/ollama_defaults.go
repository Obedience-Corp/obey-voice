package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// ollamaChosen reports whether ollama came from a deliberate selection (config
// key or env) rather than from the built-in default. Auto-enabling tools and
// skills is a capability change, so an install that never named a provider
// keeps them off even though the default provider is ollama.
func ollamaChosen(v *viper.Viper) bool {
	return v.InConfig("brain_provider") || os.Getenv("BRAIN_PROVIDER") != ""
}

// applyOllamaDefaults enables local Ollama capabilities when the user has not
// explicitly configured them. An explicit false still wins, and remote serve
// remains default-deny via remote_tools_enabled. chosen gates the auto-enable
// on a deliberate provider selection — see ollamaChosen.
//
// Skills auto-on discovers project/user SKILL.md into the Ollama system prompt
// when tools are also available; allowed-tools only constrain tools *after*
// activation. Pre-activation base tools remain full when voice_tools_enabled.
func applyOllamaDefaults(cfg *Config, v *viper.Viper, chosen bool) {
	if !chosen || !strings.EqualFold(strings.TrimSpace(cfg.BrainProvider), "ollama") {
		return
	}

	if os.Getenv("VOICE_TOOLS_ENABLED") == "" && !v.InConfig("voice_tools_enabled") {
		// Ollama's tools (list/read/write/run_command) are the only path to
		// file I/O for local models; leaving them off makes tool-capable models
		// look broken.
		cfg.VoiceToolsEnabled = true
	}

	if os.Getenv("SKILLS_ENABLED") == "" && !v.InConfig("skills_enabled") {
		// Skills are instruction catalogs loaded into the prompt. Tool power is
		// still gated by voice_tools_enabled; allow-lists apply only after
		// read_skill activation.
		cfg.SkillsEnabled = true
	}
}

// ApplyOllamaDefaults re-applies Ollama auto-enable rules to cfg using the
// live viper state (explicit config/env still win). Call after switching
// brain_provider so Settings display matches the next conversation Load().
// Callers reach here by naming a provider (persona brain block, Settings), so
// this counts as a deliberate selection.
func ApplyOllamaDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	applyOllamaDefaults(cfg, v, true)
}

// SetAndSaveBrainProvider changes the brain provider and persists the provider
// defaults that come with it. Switching TO Ollama turns tools and skills on
// unless the environment explicitly forces them off (VOICE_TOOLS_ENABLED=false),
// because full config dumps often baked voice_tools_enabled: false into the
// file as a written default, which made InConfig true and left Ollama
// tool-less.
//
// It writes through the surgical writer, one key at a time. It used to call
// save() -> viper.WriteConfigAs, which replaced config.yaml with an
// alphabetized dump of every setting viper knew: comments gone, blank lines
// gone, every built-in default frozen into the file as if the user had chosen
// it. That is the defect M1 was filed about, on the one config path the TUI's
// Settings screen and the Mac app can both reach.
func SetAndSaveBrainProvider(cfg *Config, provider string) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}

	next := *cfg
	next.BrainProvider = provider
	mu.RLock()
	applyOllamaDefaults(&next, v, true)
	mu.RUnlock()

	writes := []struct {
		key   string
		value any
	}{{key: "brain_provider", value: provider}}
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		if !envForcesFalse("VOICE_TOOLS_ENABLED") {
			writes = append(writes, struct {
				key   string
				value any
			}{key: "voice_tools_enabled", value: true})
			next.VoiceToolsEnabled = true
		}
		if !envForcesFalse("SKILLS_ENABLED") {
			writes = append(writes, struct {
				key   string
				value any
			}{key: "skills_enabled", value: true})
			next.SkillsEnabled = true
		}
	}
	for _, write := range writes {
		if _, err := SetKeyFileValue(write.key, write.value); err != nil {
			return err
		}
	}
	*cfg = next
	return nil
}
