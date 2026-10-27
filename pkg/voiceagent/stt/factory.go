package stt

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// ProviderSpec describes an STT provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

var providerSpecs = []ProviderSpec{
	{Name: "sherpa", Description: "Local sherpa-onnx Whisper (utterance-final)"},
	{Name: "sherpa-streaming", Description: "Local sherpa-onnx streaming Zipformer"},
	{Name: "sherpa-parakeet", Description: "Local sherpa-onnx NVIDIA Parakeet TDT (utterance-final, fast, no caption hallucinations)"},
	{Name: "sherpa-offline", Description: "Local sherpa-onnx Whisper (legacy alias)"},
	{Name: "whispercpp", Description: "Local whisper.cpp CLI"},
	{Name: "transcribecpp", Description: "Local transcribe.cpp CLI running NVIDIA Parakeet TDT as GGUF (optional; needs a GPU, and models ensure installs the CLI on Apple Silicon)"},
}

// Providers returns the list of implemented STT providers.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, len(providerSpecs))
	copy(out, providerSpecs)
	return out
}

// NewProvider constructs the configured STT provider and its cleanup hook.
// When config yields custom words, the provider is wrapped so every final
// transcript is corrected before it is emitted; see CustomWordsFromConfig.
func NewProvider(cfg *config.Config, capture audioSource, vad *audio.VAD) (Provider, func(), error) {
	if err := config.ValidateAcousticName(cfg); err != nil {
		return nil, nil, err
	}
	provider, cleanup, err := newBaseProvider(cfg, capture, vad)
	if err != nil {
		return nil, nil, err
	}
	return withConfiguredCorrection(provider, cfg), cleanup, nil
}

func newBaseProvider(cfg *config.Config, capture audioSource, vad *audio.VAD) (Provider, func(), error) {
	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		if _, ok := config.NormalizeSTT(cfg.STTProvider); !ok {
			return nil, nil, unsupportedProviderError(cfg.STTProvider)
		}
		return nil, nil, err
	}

	switch {
	case norm.Provider == config.STTProviderSherpa && norm.Mode == config.STTModeStreaming:
		if capture == nil {
			return nil, nil, fmt.Errorf("sherpa-streaming STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("sherpa-streaming STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}

		provider, err := NewSherpaStreamingSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case norm.Provider == config.STTProviderSherpa && norm.Mode == config.STTModeOffline:
		if capture == nil {
			return nil, nil, fmt.Errorf("sherpa STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("sherpa STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}

		provider, err := NewSherpaOfflineSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case norm.Provider == config.STTProviderParakeet:
		if capture == nil {
			return nil, nil, fmt.Errorf("sherpa-parakeet STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("sherpa-parakeet STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}
		provider, err := NewParakeetSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case norm.Provider == config.STTProviderWhisperCPP:
		if capture == nil {
			return nil, nil, fmt.Errorf("whispercpp STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("whispercpp STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}

		provider, err := NewWhisperCPPSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, nil, nil
	case norm.Provider == config.STTProviderTranscribeCPP:
		if capture == nil {
			return nil, nil, fmt.Errorf("transcribecpp STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("transcribecpp STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}

		provider, err := NewTranscribeCPPSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, nil, nil
	default:
		return nil, nil, unsupportedProviderError(cfg.STTProvider)
	}
}

func unsupportedProviderError(configured string) error {
	names := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported stt_provider %q (implemented providers: %s)", configured, strings.Join(names, ", "))
}
