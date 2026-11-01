package persona

import (
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// UseExperimentalNative saves an explicit Native default preference. It does not
// activate a running conversation: the native-aware host must prepare resources
// before capture. Only identity is persisted; global Standard provider/worker
// choices remain untouched. Use retains its guard for hosts without integration.
func UseExperimentalNative(cfg *config.Config, id string) error {
	if cfg == nil {
		return errors.New("persona: config is nil")
	}
	p, err := Load(id)
	if err != nil {
		return err
	}
	if err := RequireEnabled(p); err != nil && !errors.Is(err, ErrNativeAudioUnavailable) {
		return err
	}
	_, mode, err := ResolveAudioConfig(cfg, p)
	if err != nil {
		return err
	}
	if mode.Mode != AudioNative {
		return errors.New("explicit native selection requires a Native persona")
	}
	if err := config.SetAndSave("active_persona", id); err != nil {
		return fmt.Errorf("saving native persona preference: %w", err)
	}
	cfg.ActivePersona = id
	cfg.AgentName = p.DisplayName
	if p.Prompts.Persona != "" {
		cfg.Persona = p.Prompts.Persona
	}
	fillDisplayNames(cfg)
	return nil
}
