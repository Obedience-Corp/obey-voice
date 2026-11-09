package netapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestProviderStatusRetainsCompatibleInfoEnvelope(t *testing.T) {
	status := events.NewProviderStatus("ollama", "brain", "model", "http://user:secret@example.test/private", "unavailable", "Check Ollama")
	envelope := encodeEvent(events.Info{Message: status.Message, ProviderStatus: status})
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	if decoded["type"] != "info" || decoded["message"] != status.Message || decoded["provider_status"] == nil {
		t.Fatalf("wire=%s", data)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private") {
		t.Fatalf("endpoint leaked: %s", data)
	}
}
