package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
)

func listPreviewModel(t *testing.T) personasModel {
	t.Helper()
	m := formScreenModel(t)
	m.cfg.TTSVoice = "active-persona-override"
	m.items = []*persona.Profile{{ID: "santa", DisplayName: "Santa", TTS: persona.TTS{Provider: "kokoro", Voice: "am_santa"}}, {ID: "inherited", DisplayName: "Inherited"}}
	m.loadDefaults = func() (*config.Config, error) {
		return &config.Config{TTSProvider: "kokoro", TTSVoice: "af_bella"}, nil
	}
	m.reloadStacks()
	m.preview.newPreviewPlayer = func() audio.Engine { return newFakePreviewPlayer() }
	m.preview.ensure = func(context.Context, *config.Config, func(string, float64)) error { return nil }
	m.preview.newTTS = func(*config.Config) (tts.Provider, func(), error) { return fakePreviewTTS{}, nil, nil }
	m.usePersona = func(*config.Config, string) error { t.Fatal("preview activated persona"); return nil }
	return m
}

func TestPersonaListPreviewResolvesHighlightedVoiceWithoutActivation(t *testing.T) {
	m := listPreviewModel(t)
	defer m.preview.close()
	for i, want := range []string{"am_santa", "af_bella"} {
		m.cursor = i
		var got string
		m.preview.newTTS = func(cfg *config.Config) (tts.Provider, func(), error) {
			got = cfg.TTSVoice
			return fakePreviewTTS{}, nil, nil
		}
		var cmd tea.Cmd
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		if cmd == nil {
			t.Fatal("list p did not start preview")
		}
		m, _ = m.Update(cmd())
		if got != want || m.formMode != "" || m.cfg.ActivePersona != "samantha" || m.cfg.TTSVoice != "active-persona-override" {
			t.Fatalf("preview=%q want=%q form=%q active=%q", got, want, m.formMode, m.cfg.ActivePersona)
		}
	}
	if !strings.Contains(stripANSI(m.View()), "p preview/stop") {
		t.Fatal("list help hides preview")
	}
}

func TestPersonaListPreviewStopsAndIgnoresLateCompletion(t *testing.T) {
	for _, key := range []string{"p", "down", "up", "j", "k", "e", "m", "n", "c", "d", "x", "enter", "esc", "q"} {
		t.Run(key, func(t *testing.T) {
			m := listPreviewModel(t)
			m.usePersona = func(*config.Config, string) error { return nil }
			m.setInactive = func(*config.Config, string, bool) (*persona.Profile, error) { return m.items[0], nil }
			canceled := false
			m.preview.previewing = "am_santa"
			m.preview.previewID = 7
			m.preview.previewCancel = func() { canceled = true }
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
			switch key {
			case "down":
				msg.Type = tea.KeyDown
			case "up":
				msg.Type = tea.KeyUp
			case "enter":
				msg.Type = tea.KeyEnter
			case "esc":
				msg.Type = tea.KeyEsc
			}
			m, _ = m.Update(msg)
			if !canceled || m.preview.previewing != "" {
				t.Fatalf("%s left preview running", key)
			}
			before := m.message
			m, _ = m.Update(voicePreviewDoneMsg{id: 7, voice: "am_santa", message: "late error"})
			if m.message != before {
				t.Fatal("late completion overwrote current UI")
			}
		})
	}
}

func TestPersonaListPreviewUnavailableAndRetry(t *testing.T) {
	m := listPreviewModel(t)
	defer m.preview.close()
	m.cursor = len(m.items)
	if cmd := m.previewSelectedPersona(); cmd != nil {
		t.Fatal("create row started audio")
	}
	m.cursor = 0
	m.loadDefaults = func() (*config.Config, error) { return nil, errors.New("bad settings") }
	m.reloadStacks()
	if cmd := m.previewSelectedPersona(); cmd != nil || !strings.Contains(m.message, "Settings unavailable") {
		t.Fatal("used active config after defaults failed")
	}
	m = listPreviewModel(t)
	m.preview.newTTS = func(*config.Config) (tts.Provider, func(), error) { return nil, nil, errors.New("offline") }
	cmd := m.previewSelectedPersona()
	m, _ = m.Update(cmd())
	if m.preview.previewing != "" || !strings.Contains(m.message, "offline") {
		t.Fatal("failed preview did not recover")
	}
	m.preview.newTTS = func(*config.Config) (tts.Provider, func(), error) { return fakePreviewTTS{}, nil, nil }
	cmd = m.previewSelectedPersona()
	m, _ = m.Update(cmd())
	if !strings.Contains(m.message, "Previewed am_santa") {
		t.Fatal(m.message)
	}
}
