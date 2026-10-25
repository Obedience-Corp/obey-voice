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

// OpenAISpeechVoicesFile is Gradium's descriptive voice catalog (name, locale,
// gender, age, description per line), relative to models_dir.
const OpenAISpeechVoicesFile = "phonon/voices.jsonl"

// openAISpeechVoicesURL is where `models ensure` fetches that catalog.
var openAISpeechVoicesURL = "https://huggingface.co/gradium/phonon-voices/resolve/main/voices.jsonl"

// OpenAISpeechSelected reports whether cfg speaks through openai-speech.
func OpenAISpeechSelected(cfg *Config) bool {
	return cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), OpenAISpeechProviderName)
}

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
