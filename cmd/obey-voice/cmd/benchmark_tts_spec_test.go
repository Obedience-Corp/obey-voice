//go:build !integration

package cmd

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestParseTTSSpecs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []ttsSpec
		wantErr bool
	}{
		{
			// No sweep means one leg on the configured provider, so every
			// existing invocation keeps its single-run behaviour.
			name: "no values keeps the configured provider",
			in:   nil,
			want: []ttsSpec{{}},
		},
		{
			name: "provider without a tier",
			in:   []string{"kokoro"},
			want: []ttsSpec{{provider: "kokoro"}},
		},
		{
			name: "provider with a tier",
			in:   []string{"qwen3-tts:1.7b"},
			want: []ttsSpec{{provider: "qwen3-tts", tier: "1.7b"}},
		},
		{
			name: "tiers of one provider are distinct legs",
			in:   []string{"qwen3-tts:0.6b", "qwen3-tts:1.7b"},
			want: []ttsSpec{{provider: "qwen3-tts", tier: "0.6b"}, {provider: "qwen3-tts", tier: "1.7b"}},
		},
		{name: "empty value", in: []string{""}, wantErr: true},
		{name: "missing provider", in: []string{":1.7b"}, wantErr: true},
		{name: "empty tier", in: []string{"qwen3-tts:"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTTSSpecs(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTTSSpecs(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTTSSpecs(%q) error: %v", tt.in, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseTTSSpecs(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("leg %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTTSSpecApply(t *testing.T) {
	t.Run("empty spec leaves the config alone", func(t *testing.T) {
		cfg := config.Config{TTSProvider: "kokoro", QwenTTSModelTier: "0.6b"}
		ttsSpec{}.apply(&cfg)
		if cfg.TTSProvider != "kokoro" || cfg.QwenTTSModelTier != "0.6b" {
			t.Fatalf("apply() mutated config: %+v", cfg)
		}
	})

	t.Run("provider without a tier keeps the configured tier", func(t *testing.T) {
		// Selecting kokoro must not silently rewrite the qwen tier: the next
		// leg of the same sweep would then measure the wrong model size.
		cfg := config.Config{TTSProvider: "qwen3-tts", QwenTTSModelTier: "1.7b"}
		ttsSpec{provider: "kokoro"}.apply(&cfg)
		if cfg.TTSProvider != "kokoro" {
			t.Errorf("TTSProvider = %q, want kokoro", cfg.TTSProvider)
		}
		if cfg.QwenTTSModelTier != "1.7b" {
			t.Errorf("QwenTTSModelTier = %q, want it untouched", cfg.QwenTTSModelTier)
		}
	})

	t.Run("provider with a tier sets both", func(t *testing.T) {
		cfg := config.Config{TTSProvider: "kokoro"}
		ttsSpec{provider: "qwen3-tts", tier: "1.7b"}.apply(&cfg)
		if cfg.TTSProvider != "qwen3-tts" || cfg.QwenTTSModelTier != "1.7b" {
			t.Fatalf("apply() = %+v, want qwen3-tts/1.7b", cfg)
		}
	})
}

func TestTTSSpecString(t *testing.T) {
	tests := []struct {
		spec ttsSpec
		want string
	}{
		{ttsSpec{}, ""},
		{ttsSpec{provider: "kokoro"}, "kokoro"},
		{ttsSpec{provider: "qwen3-tts", tier: "1.7b"}, "qwen3-tts:1.7b"},
	}
	for _, tt := range tests {
		if got := tt.spec.String(); got != tt.want {
			t.Errorf("ttsSpec%+v.String() = %q, want %q", tt.spec, got, tt.want)
		}
	}
}
