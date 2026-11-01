package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestPersonaAudioFormKeepsStandardChoicesAndEditsNativeFields(t *testing.T) {
	m := formScreenModel(t)
	p := &persona.Profile{Audio: &persona.Audio{Mode: persona.AudioNative, Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}, Brain: persona.Brain{Provider: "ollama", Model: "standard-model"}, TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}}
	m.prefillStack(p)
	m.setFormRow(formRowAudioModel)
	m.formMode = "edit"
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("selected-native")})
	if m.formAudio().Model != "selected-native" || m.formBrain().Model != "standard-model" || m.formTTS().Voice != "af_heart" {
		t.Fatal("native field changed Standard choices")
	}
	m.formRow = formRowAudioMode
	m.cycleAudioRow(1)
	a := m.formAudio()
	if a.Mode != persona.AudioStandard || a.Voice != "female" || a.Transcript != persona.AudioTranscriptOff {
		t.Fatal("mode switch lost inactive native choices", a)
	}
	m.prefillStack(nil)
	if m.formAudio() != nil {
		t.Fatal("ordinary form acquired audio override")
	}
}

func TestPersonaAudioFormInvalidDraftDoesNotWrite(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m := formScreenModel(t)
	m.formMode = "edit"
	m.editID = "samantha"
	m.nameInput.SetValue("Samantha")
	m.promptTA.SetValue("You are Samantha.")
	m.prefillAudio(&persona.Profile{Audio: &persona.Audio{Mode: persona.AudioNative, Endpoint: "http://user:secret@localhost:11434"}})
	m.saveName = func(string, string) (*persona.Profile, error) { t.Fatal("invalid audio wrote name"); return nil, nil }
	m, _ = m.submitForm()
	if !strings.Contains(m.message, "endpoint") || m.formRow != formRowAudioMode {
		t.Fatal("missing actionable validation", m.message)
	}
}

func TestPersonaAudioSetupLateResultCannotChangeEditedOrClosedForm(t *testing.T) {
	m := formScreenModel(t)
	m.formMode = "edit"
	m.prefillAudio(&persona.Profile{Audio: &persona.Audio{Mode: persona.AudioNative}})
	m.audioSetupSeq = 3
	original := *m.formAudio()
	m.audioDraft.voice.SetValue("new-voice")
	m.message = "Editing"
	m.receiveAudioSetup(personaAudioSetupMsg{seq: 3, audio: original})
	if m.message != "Editing" {
		t.Fatal("old setup published after edit")
	}
	m.audioSetupCancel = func() {}
	m.cancelAudioSetup()
	m.receiveAudioSetup(personaAudioSetupMsg{seq: 3, audio: *m.formAudio()})
	if m.message != "Editing" {
		t.Fatal("cancelled setup published")
	}
	m.formMode = ""
	m.receiveAudioSetup(personaAudioSetupMsg{seq: m.audioSetupSeq, audio: *m.formAudio()})
	if m.message != "Editing" {
		t.Fatal("closed form accepted setup")
	}
}

func TestPersonaAudioFormScrollKeepsEveryFocusedFieldAndSaveHelpVisible(t *testing.T) {
	m := formScreenModel(t)
	m.formMode = "edit"
	m.prefillStack(nil)
	for _, item := range []struct {
		row   int
		label string
	}{
		{formRowAudioMode, "Mode"}, {formRowAudioModel, "Native model"}, {formRowAudioEndpoint, "Ollama origin"}, {formRowAudioVoice, "Native voice"}, {formRowAudioControls, "Controls"}, {formRowAudioTranscript, "Transcript"}, {formRowAudioRetain, "Keep audio"}, {formRowDirectory, "Starting directory"}, {formRowPrompt, "System prompt"},
	} {
		m.setFormRow(item.row)
		view := stripANSI(m.View())
		if !strings.Contains(view, item.label) || !strings.Contains(view, "ctrl+j save") {
			t.Fatalf("focused field or save help clipped: row%d\n%s", item.row, view)
		}
		if isAudioRow(item.row) && len(strings.Split(view, "\n")) > m.height {
			t.Fatalf("form exceeds terminal height: row%d", item.row)
		}
	}
}
