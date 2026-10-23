package voiceagent

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apmfrontend"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// NewVoiceFrontend builds the configured capture processing for conversations
// and launcher enrollment. Matching audio processing keeps a voiceprint from
// depending on which screen started enrollment. The caller owns Close, after
// capture has stopped; off returns nil. Unsupported selections fail explicitly.
func NewVoiceFrontend(cfg *config.Config) (audio.Frontend, error) {
	if err := config.ValidateVoiceFrontend(cfg); err != nil {
		return nil, err
	}
	switch front := config.VoiceFrontendOf(cfg); front {
	case config.VoiceFrontendOff:
		return nil, nil
	case config.VoiceFrontendWebRTC:
		primary, err := apmfrontend.New(config.VoiceFrontendNSLevelOf(cfg))
		if err != nil {
			return nil, err
		}
		return audio.NewStartupPreserveFrontend(primary), nil
	case config.VoiceFrontendNLMS:
		return audio.NewVoiceFrontend(), nil
	default:
		return nil, fmt.Errorf("unsupported voice_frontend %q", front)
	}
}
