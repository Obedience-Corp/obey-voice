package tui

import (
	"encoding/json"
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
