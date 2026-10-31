package tui

import (
	"fmt"
	ansi "github.com/charmbracelet/x/ansi"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/charmbracelet/bubbles/textinput"
)

type personaAudioDraft struct {
	value                  persona.Audio
	present                bool
	model, endpoint, voice textinput.Model
}

func (m *personasModel) prefillAudio(p *persona.Profile) {
	a := personaAudioDraft{}
	if p != nil && p.Audio != nil {
		a.value = *p.Audio
		a.present = true
	}
	a.model = textinput.New()
	a.model.SetValue(a.value.Model)
	a.model.Placeholder = "lfm3-audio:2b"
	a.endpoint = textinput.New()
	a.endpoint.SetValue(a.value.Endpoint)
	a.endpoint.Placeholder = "inherit Ollama address"
	a.voice = textinput.New()
	a.voice.SetValue(a.value.Voice)
	a.voice.Placeholder = "model default"
	m.audioDraft = a
}
func (m *personasModel) formAudio() *persona.Audio {
	a := m.audioDraft.value
	a.Model = m.audioDraft.model.Value()
	a.Endpoint = m.audioDraft.endpoint.Value()
	a.Voice = m.audioDraft.voice.Value()
	if !m.audioDraft.present && a == (persona.Audio{}) {
		return nil
	}
	return &a
}
func (m *personasModel) cycleAudioRow(delta int) {
	a := &m.audioDraft.value
	cycle := func(value *string, options ...string) {
		idx := 0
		for i, v := range options {
			if v == *value {
				idx = i
			}
		}
		*value = options[(idx+delta+len(options))%len(options)]
	}
	switch m.formRow {
	case formRowAudioMode:
		cycle(&a.Mode, persona.AudioStandard, persona.AudioNative)
	case formRowAudioControls:
		cycle(&a.Controls, persona.AudioControlsStandard, persona.AudioControlsConversation)
	case formRowAudioTranscript:
		cycle(&a.Transcript, persona.AudioTranscriptAuto, persona.AudioTranscriptRequired, persona.AudioTranscriptOff)
	case formRowAudioRetain:
		a.RetainAudio = !a.RetainAudio
	}
	m.audioDraft.present = true
	m.preview.cancel()
}
func (m personasModel) audioLines() []string {
	a := m.audioDraft.value
	mode, controls, transcript := a.Mode, a.Controls, a.Transcript
	if mode == "" {
		mode = persona.AudioStandard
	}
	if controls == "" {
		controls = persona.AudioControlsStandard
	}
	if transcript == "" {
		transcript = persona.AudioTranscriptAuto
	}
	row := func(index int, label, value string) string {
		marker := "  "
		if m.formRow == index {
			marker = "› "
		}
		return marker + fmt.Sprintf("%-13s %s", label, value)
	}
	if !isAudioRow(m.formRow) {
		return []string{"Mode: " + mode + " · native settings available below the Standard stack"}
	}
	return []string{
		row(formRowAudioMode, "Mode", "‹ "+mode+" ›"),
		row(formRowAudioModel, "Native model", m.audioDraft.model.View()),
		row(formRowAudioEndpoint, "Ollama origin", m.audioDraft.endpoint.View()),
		row(formRowAudioVoice, "Native voice", m.audioDraft.voice.View()),
		row(formRowAudioControls, "Controls", "‹ "+controls+" ›"),
		row(formRowAudioTranscript, "Transcript", "‹ "+transcript+" ›"),
		row(formRowAudioRetain, "Keep audio", fmt.Sprint(a.RetainAudio)),
		dimStyle.Render("Experimental: may make inaccurate or unperformed-action claims."),
		dimStyle.Render("Standard engines and worker choices stay saved separately."),
	}
}
func isAudioRow(row int) bool { return row >= formRowAudioMode && row <= formRowAudioRetain }
func isAudioCycleRow(row int) bool {
	return row == formRowAudioMode || row == formRowAudioControls || row == formRowAudioTranscript || row == formRowAudioRetain
}

// visibleFormLines scrolls the form around the active field without clipping
// its editing controls or the screen's status/help footer.
func (m personasModel) visibleFormLines() []string {
	lines := m.formLines()
	if !isAudioRow(m.formRow) {
		return lines
	}
	budget := max(m.height-6, 4)
	if m.height <= 0 || len(lines) <= budget {
		return lines
	}
	focus := 3
	if m.formRow == formRowPrompt {
		focus = len(lines) - 3
	} else {
		for i, line := range lines {
			plain := ansi.Strip(line)
			if strings.Contains(plain, "▸ ") || strings.Contains(plain, "› ") {
				focus = i
				break
			}
		}
	}
	start := max(min(focus-budget/2, len(lines)-budget), 0)
	return lines[start : start+budget]
}
