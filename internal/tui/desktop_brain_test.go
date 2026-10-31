package tui

import (
	"encoding/json"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestDesktopBrainSnapshotFollowsActiveSelection(t *testing.T) {
	m := desktopModel{app: App{conversation: newConversation("Obey")}, rt: fakeRuntime()}
	m.app.conversation.cfg = &config.Config{BrainProvider: "ollama", OllamaModel: "initial-model"}
	check := func(provider, model string) {
		t.Helper()
		data, err := json.Marshal(m.state())
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if wire["brain_provider"] != provider || (model != "" && wire["brain_model"] != model) || (model == "" && wire["brain_model"] != nil) {
			t.Fatalf("desktop brain = %v/%v, want %s/%s", wire["brain_provider"], wire["brain_model"], provider, model)
		}
	}
	check("ollama", "initial-model")
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "jarvis", DisplayName: "Jarvis", Revision: 2, BrainProvider: "cursor", BrainModel: "selected-model"})
	check("cursor", "selected-model")
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "obey", Revision: 1, BrainProvider: "ollama", BrainModel: "late-model"})
	check("cursor", "selected-model")
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "santa", Revision: 3, BrainProvider: "claude"})
	check("claude", "")
}

func TestDesktopAudioModeTracksPublishedSelectionRatherThanDraftSettings(t *testing.T) {
	m := desktopModel{app: App{conversation: newConversation("Obey")}, rt: fakeRuntime()}
	m.app.conversation.cfg = &config.Config{TTSProvider: config.OpenAISpeechProviderName}
	if m.state().AudioMode != "" {
		t.Fatal("speech provider alone implied Native mode")
	}
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "native", Revision: 2, AudioMode: "native", AudioControls: "conversation", AudioTranscript: "off"})
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "standard", Revision: 1, AudioMode: "standard"})
	state := m.state()
	if state.AudioMode != "native" || state.AudioControls != "conversation" || state.AudioTranscript != "off" {
		t.Fatal("late selection relabeled active audio mode", state.AudioMode)
	}
}

func TestDesktopPersonaSelectionFeedbackPreservesPublishedMode(t *testing.T) {
	m := desktopModel{app: App{conversation: newConversation("Samantha")}, rt: fakeRuntime()}
	m.app.conversation.handleEvent(events.PersonaSelection{PersonaID: "samantha", AudioMode: "native", Revision: 1})
	m.app.personas.message = "Failed to switch persona: recording transcript required"
	state := m.state()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["persona_message"] != m.app.personas.message || state.AudioMode != "native" {
		t.Fatal("selection feedback lost or relabeled active mode", wire)
	}
}

func TestPersonaProjectionFailureOffersTheOwningRecoveryAction(t *testing.T) {
	if got := personaSelectionFailure(fmt.Errorf("prepare flow: %w", brain.ErrAudioProjection)); got != "Switch blocked: transcribe recorded input or start a new conversation." {
		t.Fatal(got)
	}
	if got := personaSelectionFailure(fmt.Errorf("prepare flow: %w", brain.ErrImageProjection)); !strings.Contains(got, "saved images") {
		t.Fatal(got)
	}
}
