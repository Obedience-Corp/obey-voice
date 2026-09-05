package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func TestPersonasVoiceRowPStartsPreview(t *testing.T) {
	player := newFakePreviewPlayer()
	m := formScreenModel(t)
	m.cfg.TTSVoice = "af_heart"
	m.preview.newPreviewPlayer = func() audio.Engine { return player }
	m.preview.ensure = func(context.Context, *config.Config, func(string, float64)) error { return nil }
	m.preview.newTTS = func(*config.Config) (tts.Provider, func(), error) {
		return fakePreviewTTS{}, nil, nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m.setFormRow(formRowVoice)

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Fatal("p on the Voice row did not start a preview")
	}
	if m.preview.previewing == "" {
		t.Fatal("previewing was not set")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("preview command returned nil")
	}
}

func TestPersonasNameRowPTypesLetter(t *testing.T) {
	m := formScreenModel(t)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m.setFormRow(formRowName)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !strings.Contains(m.nameInput.Value(), "p") {
		t.Fatalf("name = %q, want p typed", m.nameInput.Value())
	}
	if m.preview.previewing != "" {
		t.Fatal("p on the Name row started a preview")
	}
}

func TestPersonasDefaultVoicePreviewsSettingsVoice(t *testing.T) {
	player := newFakePreviewPlayer()
	var previewed string
	m := formScreenModel(t)
	m.cfg.TTSProvider = "kokoro"
	m.cfg.TTSVoice = "af_bella"
	m.selectedVoice = ""
	m.preview.newPreviewPlayer = func() audio.Engine { return player }
	m.preview.ensure = func(context.Context, *config.Config, func(string, float64)) error { return nil }
	m.preview.newTTS = func(cfg *config.Config) (tts.Provider, func(), error) {
		previewed = cfg.TTSVoice
		return fakePreviewTTS{}, nil, nil
	}
	m.beginCreate()
	m.setFormRow(formRowVoice)
	cmd := m.startPersonaPreview()
	if cmd == nil {
		t.Fatal("default voice did not start a preview")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("preview command returned nil")
	}
	if previewed != "af_bella" {
		t.Fatalf("previewed voice = %q, want af_bella from Settings", previewed)
	}
}

func TestPersonasPreviewDoneGating(t *testing.T) {
	m := formScreenModel(t)
	m.preview.previewing = "af_bella"
	m.preview.previewID = 2
	m.message = "playing"

	m, _ = m.Update(voicePreviewDoneMsg{id: 1, voice: "af_heart", message: "Previewed af_heart"})
	if m.preview.previewing != "af_bella" {
		t.Fatalf("stale completion cleared previewing: %q", m.preview.previewing)
	}
	if m.message != "playing" {
		t.Fatalf("stale completion overwrote message: %q", m.message)
	}
	m, _ = m.Update(voicePreviewDoneMsg{id: 2, voice: "af_bella", message: "Previewed af_bella"})
	if m.preview.previewing != "" {
		t.Fatal("matching completion did not clear previewing")
	}
	if m.message != "Previewed af_bella" {
		t.Fatalf("message = %q", m.message)
	}
}

func TestPersonasVoiceCycleCancelsPreview(t *testing.T) {
	cancelled := false
	m := formScreenModel(t)
	m.beginCreate()
	m.setFormRow(formRowVoice)
	m.preview.previewing = "af_heart"
	m.preview.previewCancel = func() { cancelled = true }

	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if !cancelled {
		t.Fatal("←/→ on the Voice row did not cancel the in-flight preview")
	}
}

func TestPersonasLeavingFormClosesPreview(t *testing.T) {
	closed := false
	player := newFakePreviewPlayer()
	m := formScreenModel(t)
	m.beginCreate()
	m.preview.previewPlayer = player
	m.preview.previewCancel = func() { closed = true }
	m.cancelForm()
	if !closed {
		t.Fatal("leaving the form did not cancel preview")
	}
	if player.closeCount != 1 {
		t.Fatalf("player close count = %d, want 1", player.closeCount)
	}
}

func TestPersonasVoiceHelpMentionsPreview(t *testing.T) {
	m := formScreenModel(t)
	m.beginCreate()
	m.setFormRow(formRowVoice)
	view := stripANSI(m.View())
	if !strings.Contains(view, "p preview") {
		t.Fatalf("voice row help missing p preview:\n%s", view)
	}
}
