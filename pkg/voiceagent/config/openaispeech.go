package config

import "strings"

// OpenAISpeechProviderName is the tts_provider value for the OpenAI-compatible
// speech client. Phonon served by Ollama is the default target; Gradium's
// ptts-openai-server and any other /v1/audio/speech host work the same way.
const OpenAISpeechProviderName = "openai-speech"

// Defaults for the OpenAI-compatible speech client: Ollama on this computer
// serving Phonon's one shipped voice.
const (
	DefaultOpenAISpeechURL     = "http://localhost:11434/v1"
	DefaultOpenAISpeechModel   = "phonon"
	DefaultOpenAISpeechVoice   = "default"
	DefaultOpenAISpeechTimeout = 60
)

// OpenAISpeechURL is the configured base URL without a trailing slash, so
// "/audio/speech" can be appended whether or not the user typed one.
func OpenAISpeechURL(cfg *Config) string {
	url := ""
	if cfg != nil {
		url = strings.TrimSpace(cfg.OpenAISpeechURL)
	}
	if url == "" {
		url = DefaultOpenAISpeechURL
	}
	return strings.TrimRight(url, "/")
}

// OpenAISpeechModel is the configured model id, defaulting to phonon.
func OpenAISpeechModel(cfg *Config) string {
	if cfg != nil {
		if m := strings.TrimSpace(cfg.OpenAISpeechModel); m != "" {
			return m
		}
	}
	return DefaultOpenAISpeechModel
}
