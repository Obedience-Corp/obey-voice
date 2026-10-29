package discovery

import (
	"os/exec"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The SDK has no model catalog API. Keep operator-configured model IDs instead
// of guessing daemon provider names or treating a CLI default as a real model.
func discoverObey(cfg *config.Config) ProviderInfo {
	_, err := exec.LookPath("obey")
	info := ProviderInfo{Name: "obey", Available: err == nil}
	if model := strings.TrimSpace(cfg.ObeyModel); model != "" {
		info.Models = []string{model}
	}
	return info
}
