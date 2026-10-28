//go:build integration

package personaruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// This is private classifier inference against a selected local model, not a
// microphone test. Every profile, history and persisted selection is disposable.
func TestLiveHumanFollowupRouting(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_ADDRESS_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_ADDRESS_MODEL to an installed Ollama model")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434"
	}
	root := t.TempDir()
	live, err := brain.NewOllama(&config.Config{OllamaModel: model, OllamaHost: host, OllamaNumCtx: 8192, OllamaNumBatch: 128,
		AgentName: "Obey", Persona: "obey", UserDisplayName: "Lance", WorkDir: root, PromptsDir: filepath.Join(root, "prompts")})
	if err != nil {
		t.Fatal(err)
	}
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	f.selector.opts.Base.UserDisplayName = "Lance"
	f.pipeline.Brain.LoadHistory([]brain.Turn{
		{Role: "user", Content: "How's it going?", Speaker: "unknown"},
		{Role: "assistant", Content: "Hey Lance, just checking in. What can I help you with today?", PersonaDisplayName: "Obey"},
		{Role: "user", Content: "Can you help me create a new persona for Obey Voice?", Speaker: "unknown"},
		{Role: "assistant", Content: "Sure thing, Lance. Could you tell me what you'd like the persona to be called and what behavior you want it to have?", PersonaDisplayName: "Obey"},
	})
	f.selector.opts.AddressResolver = addressResolverFunc(func(ctx context.Context, req brain.AddressRequest) (brain.Addressee, error) {
		got, err := live.ResolveAddressee(ctx, req)
		t.Logf("input=%q classifier=%+v err=%v", req.Text, got, err)
		return got, err
	})
	for _, input := range []string{
		"Let's talk about it a little bit. What do you think we should do?",
		"Let's talk about it a little bit. What do you think it should do?",
		"What would you suggest?",
	} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != "obey" || got.Request != input || got.LocalReply != "" {
			t.Fatalf("admission=%+v err=%v", got, err)
		}
	}
}
