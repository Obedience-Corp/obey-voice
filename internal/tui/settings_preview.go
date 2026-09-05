package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// cancelPreview stops any in-flight voice preview. Safe to call when idle.
func (m *settingsModel) cancelPreview() {
	m.preview.cancel()
}

func (m *settingsModel) closePreview() {
	m.preview.close()
	if m.qwenInstallCancel != nil {
		m.qwenInstallCancel()
		m.qwenInstallCancel = nil
		m.qwenInstalling = false
		m.qwenInstallEvents = nil
	}
}

func (m *settingsModel) playerForPreview() audio.Engine {
	return m.preview.player()
}

func (m *settingsModel) startSettingsPreview(voice tts.Voice) tea.Cmd {
	if m.cfg == nil {
		return nil
	}
	m.preview.ensure = m.ensureTTSAssets
	m.preview.newTTS = m.newTTSProvider
	if m.preview.newPreviewPlayer == nil {
		m.preview.newPreviewPlayer = func() audio.Engine {
			return audio.NewPlayerWithDevice(m.cfg.OutputDevice)
		}
	}
	return m.preview.start(*m.cfg, voice)
}
