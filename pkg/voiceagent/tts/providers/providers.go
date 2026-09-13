// Package providers selects a TTS provider by configured name.
//
// It exists because a provider package imports the contract in
// [github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts], so the contract
// package cannot import providers back. Selection is therefore the one thing
// that has to sit above both. Keeping it in its own package means a provider
// can only go missing by editing this file — not by forgetting a blank import
// somewhere, which would compile and then fail at runtime with "unsupported
// provider" on a machine that had the model installed.
package providers

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/qwen"
)

// NewProvider constructs the configured TTS provider and its cleanup hook.
func NewProvider(cfg *config.Config) (tts.Provider, func(), error) {
	switch strings.TrimSpace(strings.ToLower(cfg.TTSProvider)) {
	case "", "kokoro":
		return tts.NewKokoroProvider(cfg)
	case "qwen3-tts":
		provider, err := qwen.NewQwen3TTS(cfg)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case tts.Audio8ProviderName:
		return tts.NewAudio8Provider(cfg)
	default:
		return nil, nil, tts.UnsupportedProvider(cfg.TTSProvider)
	}
}
