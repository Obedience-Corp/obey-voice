package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
)

func TestPersonaPreviewResolvesDraftWithoutMutatingDefaults(t *testing.T) {
	base := config.Config{TTSProvider: "kokoro", TTSVoice: "af_bella", QwenTTSVoice: "Ryan", QwenTTSModelTier: "0.6b", OpenAISpeechVoice: "server-default", OutputDevice: "speakers", SpeechSpeed: 1.2}
	for _, tc := range []struct {
		name                  string
		draft                 persona.TTS
		provider, voice, tier string
	}{
		{"inherit", persona.TTS{}, "kokoro", "af_bella", "0.6b"},
		{"qwen draft", persona.TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"}, "qwen3-tts", "Uncle_Fu", "1.7b"},
		{"provider default", persona.TTS{Provider: "qwen3-tts"}, "qwen3-tts", "Ryan", "0.6b"},
		{"remote voice", persona.TTS{Provider: "openai-speech", Voice: "custom-speaker"}, "openai-speech", "custom-speaker", "0.6b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := base
			cfg, voice, err := personaPreviewConfig(&base, tc.draft)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TTSProvider != tc.provider || voice.Name != tc.voice || config.TTSVoiceFor(&cfg, tc.provider) != tc.voice || cfg.QwenTTSModelTier != tc.tier {
				t.Fatalf("wrong speech selection: %s / %s / %s", cfg.TTSProvider, voice.Name, cfg.QwenTTSModelTier)
			}
			if cfg.OutputDevice != base.OutputDevice || cfg.SpeechSpeed != base.SpeechSpeed {
				t.Fatal("lost output preferences")
			}
			if !reflect.DeepEqual(base, before) {
				t.Fatal("preview changed defaults")
			}
		})
	}
	if _, _, err := personaPreviewConfig(&base, persona.TTS{Tier: "invalid"}); err == nil {
		t.Fatal("invalid tier accepted")
	}
}

func TestPersonaPreviewStopsAndRetiresCompletion(t *testing.T) {
	for _, row := range []int{formRowVoice, formRowTTSProvider, formRowTier} {
		m := formScreenModel(t)
		m.beginCreate()
		m.setFormRow(row)
		stopped := false
		m.preview.previewing = "af_bella"
		m.preview.previewID = 7
		m.preview.previewCancel = func() { stopped = true }
		key := tea.KeyMsg{Type: tea.KeyRight}
		if row == formRowVoice {
			key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
		}
		m, cmd := m.updateForm(key)
		if row == formRowVoice && cmd != nil {
			t.Fatal("stop started another preview")
		}
		if !stopped || m.preview.previewing != "" {
			t.Fatal("preview not stopped")
		}
		message := m.message
		m, _ = m.Update(voicePreviewDoneMsg{id: 7, voice: "af_bella", message: "late failure"})
		if m.message != message {
			t.Fatal("stale completion replaced status")
		}
	}
}

func TestPersonaCreateAndEditPreviewUseDraftAndReportFailure(t *testing.T) {
	for _, edit := range []bool{false, true} {
		m := formScreenModel(t)
		if edit {
			m.beginEditAt(formRowVoice)
		} else {
			m.beginCreate()
		}
		m.setFormRow(formRowVoice)
		m.ttsProviderIdx = providerIndex(stackTTSProviders(), "qwen3-tts")
		m.selectedVoice, m.selectedTier = "Uncle_Fu", "1.7b"
		m.preview.newPreviewPlayer = func() audio.Engine { return newFakePreviewPlayer() }
		m.preview.ensure = func(_ context.Context, c *config.Config, _ func(string, float64)) error {
			if c.QwenTTSVoice != "Uncle_Fu" || c.QwenTTSModelTier != "1.7b" {
				t.Fatalf("preview ignored draft")
			}
			return errors.New("test assets unavailable")
		}
		m, cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		if cmd == nil {
			t.Fatal("no preview")
		}
		m, _ = m.Update(cmd())
		if !strings.Contains(m.message, "test assets unavailable") || m.preview.previewing != "" {
			t.Fatalf("failure not shown: %s", m.message)
		}
		if m.formMode == "" || m.selectedVoice != "Uncle_Fu" {
			t.Fatal("failure discarded draft")
		}
	}
}

func TestPreviewCancelledDuringAssetsNeverConstructsProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ensure := func(context.Context, *config.Config, func(string, float64)) error { cancel(); return nil }
	factory := func(*config.Config) (tts.Provider, func(), error) {
		t.Fatal("provider constructed after cancellation")
		return nil, nil, nil
	}
	err := runVoicePreview(ctx, tts.Voice{Name: "test"}, newFakePreviewPlayer(), &config.Config{}, ensure, factory)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
