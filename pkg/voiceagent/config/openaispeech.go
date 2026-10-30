package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/phonon"
)

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
	if NativePhononSelected(cfg) {
		return phonon.Model
	}
	if cfg != nil {
		if m := strings.TrimSpace(cfg.OpenAISpeechModel); m != "" {
			return m
		}
	}
	return DefaultOpenAISpeechModel
}

// Empty runtime preserves the legacy semantics of library Config literals.
// Loaded application defaults explicitly select auto. Explicit endpoints and
// models are never replaced merely because the client runs Linux.
func NativePhononSelected(cfg *Config) bool {
	return nativePhononFor(cfg, runtime.GOOS)
}

func nativePhononAssetName(backend string) string { return "Phonon native (" + backend + ")" }

func nativePhononFor(cfg *Config, goos string) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.OpenAISpeechRuntime)) {
	case "native":
		return true
	case "auto":
		return goos == "linux" && !cfg.OpenAISpeechExternal && strings.TrimSpace(cfg.OpenAISpeechAPIKey) == "" &&
			(strings.TrimSpace(cfg.OpenAISpeechURL) == "" || strings.TrimRight(cfg.OpenAISpeechURL, "/") == DefaultOpenAISpeechURL) &&
			(strings.TrimSpace(cfg.OpenAISpeechModel) == "" || IsDefaultPhononModel(cfg.OpenAISpeechModel))
	}
	return false
}

func NativePhononOptions(cfg *Config) (phonon.Options, error) {
	if cfg == nil {
		return phonon.Options{}, fmt.Errorf("native Phonon: nil configuration")
	}
	if model := strings.TrimSpace(cfg.OpenAISpeechModel); model != "" && !IsDefaultPhononModel(model) && model != phonon.Model {
		return phonon.Options{}, fmt.Errorf("native Phonon manages %s; use openai_speech_runtime external for custom model %q", phonon.Model, model)
	}
	if url := strings.TrimRight(strings.TrimSpace(cfg.OpenAISpeechURL), "/"); url != "" && url != DefaultOpenAISpeechURL {
		return phonon.Options{}, fmt.Errorf("native Phonon owns its local endpoint; use openai_speech_runtime external for openai_speech_url %q", url)
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.OpenAISpeechBackend))
	if backend == "" || backend == "auto" {
		backend = phonon.DetectBackend()
	}
	binary := strings.TrimSpace(cfg.OpenAISpeechNativeBinary)
	if binary != "" && !filepath.IsAbs(binary) {
		return phonon.Options{}, fmt.Errorf("openai_speech_native_binary must be an absolute path")
	}
	dir, err := filepath.Abs(ModelsDirFrom(cfg))
	if err != nil {
		return phonon.Options{}, fmt.Errorf("native Phonon models directory: %w", err)
	}
	o := phonon.Options{ModelsDir: dir, Backend: backend, Binary: binary}
	return o, phonon.Validate(o)
}

func ValidateOpenAISpeechRuntime(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.OpenAISpeechRuntime)) {
	case "", "auto", "external", "native":
		return nil
	default:
		return fmt.Errorf("unsupported openai_speech_runtime %q; use auto, external or native", cfg.OpenAISpeechRuntime)
	}
}
