package tui

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	tea "github.com/charmbracelet/bubbletea"
)

type personaAudioSetupMsg struct {
	seq   uint64
	audio persona.Audio
	err   error
}

func (m *personasModel) cancelAudioSetup() {
	if m.audioSetupCancel != nil {
		m.audioSetupCancel()
		m.audioSetupCancel = nil
		m.audioSetupSeq++
	}
}
func (m *personasModel) startAudioSetup() tea.Cmd {
	if m.audioSetupCancel != nil {
		m.cancelAudioSetup()
		m.message = "Setup check cancelled"
		return nil
	}
	a := m.formAudio()
	if a == nil {
		a = &persona.Audio{}
	}
	copy := *a
	copy.Mode = persona.AudioNative
	p := &persona.Profile{ID: m.editID, DisplayName: m.nameInput.Value(), Brain: m.formBrain(), TTS: m.formTTS(), Audio: &copy}
	cfg, effective, err := persona.ResolveAudioConfig(m.cfg, p)
	if err != nil {
		m.message = err.Error()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.audioSetupCancel = cancel
	m.audioSetupSeq++
	seq := m.audioSetupSeq
	m.preview.cancel()
	m.message = "Checking native setup quietly · ctrl+g cancels"
	return func() tea.Msg {
		defer cancel()
		client, err := nativeaudio.NewClient(effective.Endpoint, effective.Model)
		if err != nil {
			return personaAudioSetupMsg{seq, *a, err}
		}
		defer client.Close()
		_, closeSpeech, _, err := nativeaudio.NewSupportCache().Prepare(ctx, client, cfg, nativeaudio.Requirements{ASR: effective.Controls == persona.AudioControlsStandard || effective.Transcript == persona.AudioTranscriptRequired, Tools: cfg.VoiceToolsEnabled && effective.Controls == persona.AudioControlsStandard})
		if closeSpeech != nil {
			closeSpeech()
		}
		return personaAudioSetupMsg{seq, *a, err}
	}
}
func (m *personasModel) receiveAudioSetup(msg personaAudioSetupMsg) {
	if msg.seq != m.audioSetupSeq || m.formMode == "" {
		return
	}
	m.audioSetupCancel = nil
	current := m.formAudio()
	if current == nil {
		current = &persona.Audio{}
	}
	if *current != msg.audio {
		return
	}
	if msg.err != nil {
		m.message = fmt.Sprintf("Native setup unavailable: %v", msg.err)
		return
	}
	m.message = "Native setup passed · experimental responses may be inaccurate or claim unperformed actions"
}
