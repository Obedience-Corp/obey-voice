package cmd

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// personaWorkDiags says, for every persona, who runs its background work:
// its own backend and model, or the worker, and why. Read-only.
func personaWorkDiags(cfg *config.Config) []config.Diagnostic {
	if cfg == nil {
		return nil
	}
	profiles, err := persona.List()
	if err != nil {
		return nil
	}
	var diags []config.Diagnostic
	for _, p := range profiles {
		if p == nil {
			continue
		}
		bound := cfg.Clone()
		persona.Apply(bound, p)
		diags = append(diags, workDiag(p.ID, bound))
	}
	return diags
}

func workDiag(id string, cfg *config.Config) config.Diagnostic {
	d := config.Diagnostic{Name: "work persona " + id, Severity: config.SeverityOK}
	reason := config.WorkReason(cfg)
	provider := strings.ToLower(strings.TrimSpace(cfg.BrainProvider))
	if provider == "" {
		provider = "ollama"
	}
	switch {
	case config.WorkOf(cfg) == config.WorkOwn && cfg.VoiceWorkEnabled && brain.IsHosted(provider):
		d.Detail = workerDetail(cfg) + "; " + reason + ", but voice_work_enabled keeps a hosted persona's work on the local worker"
	case config.WorkOf(cfg) == config.WorkOwn:
		d.Detail = fmt.Sprintf("its own %s (%s) does it; %s", provider, modelLabel(config.BrainModelFor(cfg, provider)), reason)
		if config.OwnWorkStrict(cfg) && !brain.WorkerCapabilitiesFor(provider).ToolCallMediation.Supported {
			d.Severity = config.SeverityWarn
			d.Detail += "; refused under the strict worker policy"
			d.Remediation = fmt.Sprintf("obey-voice persona edit %s --work worker, or use the advisory worker policy", id)
		}
	default:
		d.Detail = workerDetail(cfg) + "; " + reason
	}
	if cfg.VoiceWorkersDisabled {
		d.Detail += " (background workers are off)"
	}
	return d
}

func workerDetail(cfg *config.Config) string {
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		return "the worker tiers do it"
	}
	provider := config.VoiceWorkerProviderOf(cfg)
	model := config.BrainModelFor(config.WorkerBrainConfig(cfg), provider)
	return fmt.Sprintf("the worker %s (%s) does it", provider, modelLabel(model))
}

func modelLabel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default model"
	}
	return model
}
