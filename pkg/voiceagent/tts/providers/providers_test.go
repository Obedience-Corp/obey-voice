package providers

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{TTSProvider: "edge"}

	_, _, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported tts_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported tts_provider message", err)
	}
}

// Selecting audio8 must still fail before any model loading, now on the
// tokenizer file rather than on a nil tokenizer. ModelsDir points at an empty
// directory: reaching the manifest or the graphs would name
// runtime_manifest.json or a .onnx instead, so naming tokenizer.json proves the
// rejection happened first.
func TestNewProviderAudio8FailsFastWithoutTokenizerFile(t *testing.T) {
	cfg := &config.Config{TTSProvider: "audio8", ModelsDir: t.TempDir()}

	_, _, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want a tokenizer error")
	}
	if !strings.Contains(err.Error(), "audio8 tokenizer") || !strings.Contains(err.Error(), "tokenizer.json") {
		t.Fatalf("NewProvider() error = %q, want it to name the audio8 tokenizer file", err)
	}
	if strings.Contains(err.Error(), "runtime_manifest.json") || strings.Contains(err.Error(), ".onnx") {
		t.Fatalf("NewProvider() error = %q, want the failure before any model loading", err)
	}
}
