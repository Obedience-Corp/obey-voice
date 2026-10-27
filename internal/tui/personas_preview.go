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
		return config.Config{}, tts.Voice{}, fmt.Errorf("No voice to preview")
	}
	if err := persona.ValidateTier(selection.Tier); err != nil {
		return config.Config{}, tts.Voice{}, err
	}
	cfg := *base
	persona.Apply(&cfg, &persona.Profile{TTS: selection})
	cfg.TTSProvider = activeTTSProvider(&cfg)
	voice := tts.Voice{Name: config.TTSVoiceFor(&cfg, cfg.TTSProvider)}
	if voice.Name == "" {
		return cfg, voice, fmt.Errorf("No voice to preview")
	}
	applyPreviewVoice(&cfg, voice)
	return cfg, voice, nil
}

func (m *personasModel) startPersonaPreview() tea.Cmd {
	cfg, voice, err := personaPreviewConfig(m.cfg, m.formTTS())
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
