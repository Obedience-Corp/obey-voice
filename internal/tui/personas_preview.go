package tui

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
)

// personaPreviewConfig resolves the draft exactly as a persona's speech stack,
// on a copy so previewing never changes the form, defaults, or active session.
func personaPreviewConfig(base *config.Config, selection persona.TTS) (config.Config, tts.Voice, error) {
	if base == nil {
		return config.Config{}, tts.Voice{}, fmt.Errorf("no voice to preview")
	}
	if err := persona.ValidateTier(selection.Tier); err != nil {
		return config.Config{}, tts.Voice{}, err
	}
	cfg := *base
	persona.Apply(&cfg, &persona.Profile{TTS: selection})
	cfg.TTSProvider = activeTTSProvider(&cfg)
	voice := tts.Voice{Name: config.TTSVoiceFor(&cfg, cfg.TTSProvider)}
	if voice.Name == "" {
		return cfg, voice, fmt.Errorf("no voice to preview")
	}
	applyPreviewVoice(&cfg, voice)
	return cfg, voice, nil
}

func (m *personasModel) previewSelectedPersona() tea.Cmd {
	if m.preview.previewing != "" {
		m.preview.cancel()
		m.message = "Preview stopped"
		return nil
	}
	if m.loadErr != "" || m.cursor < 0 || m.cursor >= len(m.items) || m.items[m.cursor] == nil {
		return nil
	}
	if m.listDefaults == nil {
		m.message = "Settings unavailable; cannot preview voice"
		return nil
	}
	return m.startVoicePreview(m.listDefaults, m.items[m.cursor].TTS)
}

func (m *personasModel) startPersonaPreview() tea.Cmd {
	return m.startVoicePreview(m.cfg, m.formTTS())
}

func (m *personasModel) startVoicePreview(base *config.Config, selection persona.TTS) tea.Cmd {
	cfg, voice, err := personaPreviewConfig(base, selection)
	if err != nil {
		m.message = err.Error()
		return nil
	}
	m.message = "Preparing preview · p stops"
	return m.preview.start(cfg, voice)
}

// PreviewPersonaVoice plays one unsaved selection without starting a session.
// It blocks until playback and provider cleanup finish; callers run it off the UI
// thread and cancel the context to stop preparation, synthesis, or playback.
func PreviewPersonaVoice(ctx context.Context, base *config.Config, selection persona.TTS) error {
	cfg, voice, err := personaPreviewConfig(base, selection)
	if err != nil {
		return err
	}
	player := playback.NewPlayerWithDevice(cfg.OutputDevice)
	player.SetVolume(config.PlaybackVolumeOf(&cfg))
	defer player.Close()
	return runVoicePreview(ctx, voice, player, &cfg, nil, nil)
}
