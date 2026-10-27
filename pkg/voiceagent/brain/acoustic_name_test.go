package brain

import (
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAcousticHintDoesNotChangeDefaultOrAgentClassifierPrompt(t *testing.T) {
	for _, tc := range []struct{ text, account, hint, wantHint string }{
		{"Heyobe, tell me the weather.", "user", "Obey", "Obey"},
		{"You must OBEY the rules.", "user", "Obey", ""},
		{"Tell me about Obey.", "user", "Obey", ""},
		{"Oh bay, your thoughts?", "agent", "Obey", ""},
		{"Hello there", "user", "", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			var wire api.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: `{"kind":"none","name":"","bare":false}`}, Done: true})
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
			input := AddressRequest{Text: tc.text, Account: tc.account, AcousticName: tc.hint}
			if _, err := o.ResolveAddressee(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			var sent AddressRequest
			if err := json.Unmarshal([]byte(wire.Messages[1].Content), &sent); err != nil {
				t.Fatal(err)
			}
			if sent.Text != tc.text || sent.AcousticName != tc.wantHint || input.AcousticName != tc.hint {
				t.Fatalf("sent=%+v input=%+v", sent, input)
			}
			if tc.wantHint == "" && wire.Messages[0].Content != addresseeInstruction {
				t.Fatal("changed default classifier prompt")
			}
			if tc.wantHint != "" && !strings.Contains(wire.Messages[0].Content, acousticNameInstruction) {
				t.Fatal("missing evidence semantics")
			}
		})
	}
}
