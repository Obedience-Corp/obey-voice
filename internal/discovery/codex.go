package discovery

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"os/exec"
)

func discoverCodex(parent context.Context) ProviderInfo {
	info := ProviderInfo{Name: "codex"}
	if _, err := exec.LookPath("codex"); err != nil {
		return info
	}
	info.Available = true
	info.Models = []string{"default"}
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	if models, err := brain.CodexModels(ctx); err == nil && len(models) > 0 {
		info.Models = append(info.Models, models...)
	}
	return info
}
