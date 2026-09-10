package brain

import (
	"encoding/json"
	"testing"
)

// Ollama renders tool schemas at the front of the prompt and caches the
// prompt prefix. A schema whose property order changes between requests makes
// every turn a cold prompt, so each builder must serialize identically on
// every call.
func TestToolSchemasSerializeStably(t *testing.T) {
	builders := map[string]func() any{
		"live":       func() any { return liveRouterTools() },
		"assistant":  func() any { return voiceAssistantTools(nil) },
		"delegation": func() any { return delegationTools() },
	}
	for name, build := range builders {
		first, err := json.Marshal(build())
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		for i := 0; i < 50; i++ {
			again, err := json.Marshal(build())
			if err != nil {
				t.Fatalf("%s: marshal: %v", name, err)
			}
			if string(again) != string(first) {
				t.Fatalf("%s: tool schema changed between calls\nfirst: %s\nagain: %s", name, first, again)
			}
		}
	}
}
