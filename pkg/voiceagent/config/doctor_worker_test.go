package config

import (
	"strings"
	"testing"
)

func TestDiagnoseWorkerProvider(t *testing.T) {
	t.Parallel()
	look := func(string) (string, error) { return "", nil }

	t.Run("same host same model is one load two clients", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BrainProvider:       "ollama",
			VoiceWorkerProvider: "ollama",
			OllamaModel:         "qwen3:8b",
			OllamaHost:          "http://localhost:11434",
		}
		d := diagnoseWorkerProvider(cfg, look)
		if d.Severity != SeverityOK {
			t.Fatalf("severity = %q, want ok: %+v", d.Severity, d)
		}
		if !strings.Contains(d.Detail, "one load, two clients") {
			t.Fatalf("detail = %q, want one load, two clients", d.Detail)
		}
	})

	t.Run("empty worker model inherits live", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BrainProvider:       "ollama",
			VoiceWorkerProvider: "ollama",
			OllamaModel:         "qwen3:8b",
			OllamaHost:          "http://localhost:11434",
		}
		d := diagnoseWorkerProvider(cfg, look)
		if d.Severity != SeverityOK || !strings.Contains(d.Detail, "one load, two clients") {
			t.Fatalf("empty worker model: %+v", d)
		}
	})

	t.Run("same host distinct models warn", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BrainProvider:       "ollama",
			VoiceWorkerProvider: "ollama",
			OllamaModel:         "qwen3:8b",
			VoiceWorkerModel:    "qwen3:30b",
			OllamaHost:          "http://localhost:11434",
		}
		d := diagnoseWorkerProvider(cfg, look)
		if d.Severity != SeverityWarn {
			t.Fatalf("severity = %q, want warn: %+v", d.Severity, d)
		}
		if !strings.Contains(d.Detail, "two weight sets") {
			t.Fatalf("detail = %q, want two weight sets", d.Detail)
		}
	})

	t.Run("different host is ok", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BrainProvider:         "ollama",
			VoiceWorkerProvider:   "ollama",
			OllamaModel:           "qwen3:8b",
			VoiceWorkerOllamaHost: "http://studio:11434",
			OllamaHost:            "http://localhost:11434",
		}
		d := diagnoseWorkerProvider(cfg, look)
		if d.Severity != SeverityOK {
			t.Fatalf("severity = %q, want ok: %+v", d.Severity, d)
		}
		if strings.Contains(d.Detail, "one load, two clients") {
			t.Fatalf("remote host should not claim shared load: %q", d.Detail)
		}
	})

	t.Run("unsupported provider is error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{VoiceWorkerProvider: "cursor"}
		d := diagnoseWorkerProvider(cfg, look)
		if d.Severity != SeverityError {
			t.Fatalf("severity = %q, want error: %+v", d.Severity, d)
		}
	})
}
