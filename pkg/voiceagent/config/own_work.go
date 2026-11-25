package config

import (
	"fmt"
	"slices"
	"strings"
)

// Who runs a persona's background work.
const (
	// WorkOwn runs it on the persona's own backend and model, in a session
	// of its own.
	WorkOwn = "own"
	// WorkWorker hands it to the configured worker.
	WorkWorker = "worker"
	// WorkInline keeps the work in the conversation: the persona uses its
	// tools on its own turns and no background job starts from them.
	WorkInline = "inline"
)

// WorkChoices are the values brain.work accepts; empty takes the default.
var WorkChoices = []string{WorkOwn, WorkWorker, WorkInline}

// ValidateWork accepts an empty value or one of WorkChoices.
func ValidateWork(value string) error {
	if value = strings.TrimSpace(value); value == "" || slices.Contains(WorkChoices, strings.ToLower(value)) {
		return nil
	}
	return fmt.Errorf("brain.work %q: use %s, %s or %s (empty takes the default)", value, WorkOwn, WorkWorker, WorkInline)
}

func chosenWork(cfg *Config) string {
	if value := strings.ToLower(strings.TrimSpace(cfg.PersonaWork)); slices.Contains(WorkChoices, value) {
		return value
	}
	return ""
}

// DefaultWork is who does the work of a persona on provider when its
// profile does not say: the configured worker on Ollama or when the profile
// names its own worker, and the persona itself otherwise. A worker chosen
// only in Settings does not change it.
func DefaultWork(provider string, namesWorker bool) string {
	if namesWorker || brainProviderName(provider) == "ollama" {
		return WorkWorker
	}
	return WorkOwn
}

// WorkOf is who runs the bound persona's background work.
func WorkOf(cfg *Config) string {
	if cfg == nil {
		return WorkWorker
	}
	if value := chosenWork(cfg); value != "" {
		return value
	}
	return DefaultWork(cfg.BrainProvider, cfg.PersonaNamesWorker)
}

// WorksInline reports whether the bound persona does its work in the
// conversation and hands none to background jobs. A persona set to inline
// that the installation policy refuses does not.
func WorksInline(cfg *Config) bool {
	return cfg != nil && WorkOf(cfg) == WorkInline && InlineWorkRefused(cfg) == nil
}

// InlineWorkRefused is why the bound persona, set to inline, may not work
// inline: tiered mode's strict policy holds tool work to a tier's limits and
// allowlist, which a live turn's tools are not held to. Its work then goes to
// the worker tiers. It is nil otherwise.
func InlineWorkRefused(cfg *Config) error {
	if cfg == nil || WorkOf(cfg) != WorkInline || !OwnWorkStrict(cfg) {
		return nil
	}
	return fmt.Errorf("inline work is refused under the strict worker policy, which holds tool work to a tier's limits that a live turn is not held to, so the worker tiers do its work. Set its work to worker, or use the advisory policy")
}

// WorkReason says why WorkOf chose what it did, for doctor and the persona
// form.
func WorkReason(cfg *Config) string {
	if cfg == nil {
		return "no persona is bound"
	}
	if value := chosenWork(cfg); value != "" {
		return "brain.work is " + value
	}
	if cfg.PersonaNamesWorker {
		return "default: its brain.worker is set"
	}
	return "default on " + brainProviderName(cfg.BrainProvider)
}

// OwnWorkConfig is the job configuration for work the persona does itself:
// cfg with the persona's own backend, model and host in place of the
// configured worker. voice_tools_enabled is kept as set. Worker tiers do not
// choose its backend, so it is admitted as a legacy worker; OwnWorkTier holds
// it to the routed tier.
func OwnWorkConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	own := cfg.Clone()
	provider := brainProviderName(cfg.BrainProvider)
	own.BrainProvider = provider
	own.VoiceWorkerProvider = provider
	own.VoiceWorkerModel = BrainModelFor(cfg, provider)
	own.VoiceWorkerOllamaHost = ""
	// The persona's own context window, not the worker's.
	own.VoiceWorkerNumCtx = cfg.OllamaNumCtx
	if own.VoiceWorkers != nil {
		own.VoiceWorkers.Mode = WorkerModeLegacy
	}
	own.DelegationEnabled = false
	return own
}

// OwnTier is what the routed tier holds a persona's own job to in tiered
// mode: the tier's limits and the installation's charge policy. The tier's
// assignment and billing describe the worker's backend and do not apply.
type OwnTier struct {
	Tier   WorkerTierName
	Limits WorkerLimits
	Strict bool
}

// OwnWorkTier resolves the routed tier for an own job of the bound persona,
// with the persona's worker_tiers overrides applied. It is nil outside tiered
// mode, and an error when the route named no known tier or a disabled one,
// as for a worker.
func OwnWorkTier(cfg *Config, name WorkerTierName) (*OwnTier, error) {
	if cfg == nil || cfg.VoiceWorkers == nil || cfg.VoiceWorkers.Mode != WorkerModeTiered {
		return nil, nil
	}
	workers, err := cfg.EffectiveVoiceWorkers()
	if err != nil {
		return nil, err
	}
	t := workers.tier(name)
	if t == nil {
		return nil, fmt.Errorf("worker tiers are on and this job was routed to no known tier (%q)", name)
	}
	if !t.Enabled {
		return nil, fmt.Errorf("worker tier %s is disabled", name)
	}
	return &OwnTier{Tier: name, Limits: cloneTier(*t).Limits, Strict: workers.Policy.ChargePolicy == "strict"}, nil
}

// OwnWorkTiersClosed is why no tier can hold the bound persona's own jobs:
// tiered mode is on and none of its tiers, with the persona's overrides, is
// enabled. It is nil otherwise.
func OwnWorkTiersClosed(cfg *Config) error {
	if cfg == nil || cfg.VoiceWorkers == nil || cfg.VoiceWorkers.Mode != WorkerModeTiered {
		return nil
	}
	workers, err := cfg.EffectiveVoiceWorkers()
	if err != nil {
		return err
	}
	if workers.Tiers.Simple.Enabled || workers.Tiers.Medium.Enabled || workers.Tiers.Complex.Enabled {
		return nil
	}
	return fmt.Errorf("worker tiers are on and none is enabled")
}

// SettingsWorkerChosen reports whether Settings, rather than the persona,
// chose a worker for the bound persona: voice_worker_provider, or an enabled
// tier in tiered mode.
func SettingsWorkerChosen(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if w := cfg.VoiceWorkers; w != nil && w.Mode == WorkerModeTiered {
		return w.Tiers.Simple.Enabled || w.Tiers.Medium.Enabled || w.Tiers.Complex.Enabled
	}
	return !cfg.PersonaNamesWorker && WorkerSelected(cfg)
}

// OwnWorkStrict reports whether tiered mode holds the bound persona's own
// jobs to the strict worker policy.
func OwnWorkStrict(cfg *Config) bool {
	return cfg != nil && cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == WorkerModeTiered && cfg.VoiceWorkers.Policy.ChargePolicy == "strict"
}

func brainProviderName(provider string) string {
	if name := strings.ToLower(strings.TrimSpace(provider)); name != "" {
		return name
	}
	return "ollama"
}
