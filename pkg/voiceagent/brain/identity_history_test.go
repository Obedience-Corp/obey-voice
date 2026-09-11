package brain

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestOllamaCanonicalHistoryKeepsIdentityBeyondProjectionTrim(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{MaxHistory: 2}}
	turns := []Turn{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "old", PersonaID: "santa", PersonaDisplayName: "Santa", PromptHash: "p1", ResponseID: "r1"}}
	o.LoadHistory(turns)
	o.appendMessage(api.Message{Role: "user", Content: "next"}, nil)
	o.appendMessage(api.Message{Role: "assistant", Content: "new"}, &TurnIdentity{PersonaID: "sam", PersonaDisplayName: "Samantha", PromptHash: "p2", ResponseID: "r2"})
	o.appendMessage(api.Message{Role: "user", Content: "third"}, nil)
	o.trimHistory()
	got := o.History()
	if len(got) != 5 || got[1].PersonaID != "santa" || got[3].ResponseID != "r2" {
		t.Fatalf("canonical history = %#v", got)
	}
}

func TestOllamaImportHistoryDoesNotResetProjectionLifecycle(t *testing.T) {
	o := &OllamaBrain{history: []api.Message{{Role: "assistant", Content: "live"}}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "restored"}}); err != nil {
		t.Fatal(err)
	}
	if len(o.history) != 1 || o.history[0].Role != "user" {
		t.Fatalf("projection = %#v", o.history)
	}
}
