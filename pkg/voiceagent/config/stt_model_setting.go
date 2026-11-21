package config

import "fmt"

// STTModelSetting identifies the model loaded by the selected provider and mode.
// Settings use the same schema and defaults as config set.
func STTModelSetting(cfg *Config) (KeySpec, string, error) {
	norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		return KeySpec{}, "", err
	}
	var key, value string
	switch norm.Provider {
	case STTProviderParakeet:
		key, value = "parakeet_model", cfg.ParakeetModel
	case STTProviderSherpa:
		key, value = "whisper_model", cfg.WhisperModel
		if norm.Mode == STTModeStreaming {
			key, value = "sherpa_streaming_model", cfg.SherpaStreamingModel
		}
	case STTProviderWhisperCPP:
		key, value = "whispercpp_model", cfg.WhisperCPPModel
	case STTProviderTranscribeCPP:
		key, value = "transcribecpp_model", cfg.TranscribeCPPModel
	default:
		return KeySpec{}, "", fmt.Errorf("no model setting for %s", norm.Provider)
	}
	spec, _ := SpecFor(key)
	if value == "" {
		value, _ = spec.Default.(string)
	}
	return spec, value, nil
}

// SetSTTModel updates a copied configuration before installation or after saving.
func SetSTTModel(cfg *Config, key, value string) error {
	switch key {
	case "parakeet_model":
		cfg.ParakeetModel = value
	case "whisper_model":
		cfg.WhisperModel = value
	case "sherpa_streaming_model":
		cfg.SherpaStreamingModel = value
	case "whispercpp_model":
		cfg.WhisperCPPModel = value
	case "transcribecpp_model":
		cfg.TranscribeCPPModel = value
	default:
		return fmt.Errorf("unknown STT model setting %q", key)
	}
	return nil
}
