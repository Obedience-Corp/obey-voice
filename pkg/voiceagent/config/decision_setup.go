package config

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"strings"
)

// DecisionSetupConfig prepares the saved selection without enabling routing or
// changing the user's chosen provider. Explicit setup is independent of usage.
func DecisionSetupConfig(cfg *Config) *Config {
	c := cfg.Clone()
	c.DecisionEnabled = true
	c.DecisionVoiceWork = true
	c.DecisionProvider = "local"
	c.DecisionRuntime = "managed"
	return c
}

// DecisionSetupStatus verifies pinned files. Call from a worker, never a UI loop.
func DecisionSetupStatus(cfg *Config) (string, error) {
	c := DecisionSetupConfig(cfg)
	model, err := decider.ModelFor(c.DecisionLocalModel)
	if err != nil {
		return "", err
	}
	manifest, err := ManifestFor(c, AssetRequest{NeedDecision: true})
	if err != nil {
		return "", err
	}
	var missing []string
	for _, s := range manifest.Status(ModelsDirFrom(c)) {
		if !s.Installed {
			missing = append(missing, s.Name)
		}
	}
	state := "Installed and verified"
	if len(missing) > 0 {
		state = "Needs install or repair: " + strings.Join(missing, ", ")
	}
	return fmt.Sprintf("%s\n%s\nLocation: %s\n%s\nExisting verified files are reused. Runtime dependencies need additional disk space. Setup does not enable decision routing.", model.Name, model.Details(), ModelsDirFrom(c), state), nil
}
