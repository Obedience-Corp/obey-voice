package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestConversationHeaderNamesConfiguredBrain(t *testing.T) {
	for _, tt := range []struct {
		cfg  *config.Config
		want string
	}{
		{&config.Config{BrainProvider: "ollama", OllamaModel: "qwen3:8b"}, "brain ollama · qwen3:8b"},
		{&config.Config{BrainProvider: "grok", GrokModel: "grok-build"}, "brain grok · grok-build"},
		{&config.Config{BrainProvider: "cursor", CursorModel: "composer-2.5"}, "brain cursor · composer-2.5"},
		{&config.Config{BrainProvider: "codex", CodexModel: "gpt-5.4"}, "brain codex · gpt-5.4"},
		{&config.Config{BrainProvider: "claude", OllamaModel: "unused"}, "brain claude · default"},
		{&config.Config{BrainProvider: "claude", ClaudeModel: "haiku"}, "brain claude · haiku"},
	} {
		t.Run(tt.cfg.BrainProvider, func(t *testing.T) {
			m := sizedConversation(t, 120, 24)
			m.cfg = tt.cfg
			header := strings.SplitN(stripANSI(m.View()), "\n", 2)[0]
			if !strings.Contains(header, tt.want) {
				t.Fatalf("header = %q, want %q", header, tt.want)
			}
		})
	}
}

func TestConversationHeaderFollowsSelectionWithoutRegressing(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.sessionID = "current"
	m.cfg = &config.Config{BrainProvider: "ollama", OllamaModel: "startup-model"}
	m.handleEvent(events.PersonaSelection{SessionID: "current", PersonaID: "jarvis", DisplayName: "Jarvis", Revision: 2, BrainProvider: "codex", BrainModel: "selected-model"})
	m.handleEvent(events.PersonaSelection{SessionID: "current", PersonaID: "obey", DisplayName: "Obey", Revision: 1, BrainProvider: "ollama", BrainModel: "old-model"})
	m.handleEvent(events.PersonaSelection{SessionID: "retired", PersonaID: "santa", DisplayName: "Santa", Revision: 9, BrainProvider: "ollama", BrainModel: "retired-model"})
	m.handleEvent(events.ResponseReady{Response: "A delayed reply", Envelope: &events.ResponseEnvelope{DisplayName: "Obey"}})
	header := strings.SplitN(stripANSI(m.View()), "\n", 2)[0]
	if !strings.Contains(header, "Jarvis") || !strings.Contains(header, "brain codex · selected-model") {
		t.Fatalf("header lost the active binding: %q", header)
	}
	for _, stale := range []string{"startup-model", "old-model", "retired-model"} {
		if strings.Contains(header, stale) {
			t.Fatalf("header contains stale model %q: %s", stale, header)
		}
	}
	// An empty model in the new selection must not reuse the startup model.
	m.handleEvent(events.PersonaSelection{SessionID: "current", PersonaID: "obey", DisplayName: "Obey", Revision: 3, BrainProvider: "claude"})
	header = strings.SplitN(stripANSI(m.View()), "\n", 2)[0]
	if !strings.Contains(header, "brain claude · default") || strings.Contains(header, "startup-model") {
		t.Fatalf("provider default mislabeled: %s", header)
	}
}

func TestConversationModelHeaderFitsDuringSpeech(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := sizedConversation(t, width, 24)
		m.agentName = "Obey"
		m.cfg = &config.Config{BrainProvider: "ollama", OllamaModel: "qwen3:8b", TTSProvider: "openai-speech", OpenAISpeechModel: "phonon", OpenAISpeechVoice: "Harper"}
		m.setVoiceMode(anim.ModeSpeaking)
		header := strings.SplitN(m.View(), "\n", 2)[0]
		if lipgloss.Width(header) > width {
			t.Fatalf("header exceeds %d columns: %s", width, header)
		}
		if width >= 80 && !strings.Contains(stripANSI(header), "brain ollama · qwen3:8b") {
			t.Fatalf("speech meter hides model at width %d: %s", width, header)
		}
	}
}
