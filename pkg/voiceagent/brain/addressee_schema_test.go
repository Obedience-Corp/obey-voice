package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestAddresseeSchemaAndParserRestrictTargetsToRoster(t *testing.T) {
	for _, target := range []string{"LFM3", "Lance", "Obey", "Santa"} {
		t.Run(target, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
					return
				}
				var schema struct {
					Properties struct {
						Name struct {
							Enum []string `json:"enum"`
						} `json:"name"`
					} `json:"properties"`
				}
				if err := json.Unmarshal(wire.Format, &schema); err != nil {
					t.Error(err)
					return
				}
				allowed := schema.Properties.Name.Enum
				for _, name := range []string{"", "Obey", "Santa North", "Santa"} {
					if !slices.Contains(allowed, name) {
						t.Errorf("schema missing configured name %q", name)
					}
				}
				if len(allowed) != 4 || slices.Contains(allowed, "LFM3") || slices.Contains(allowed, "Lance") {
					t.Errorf("non-persona in schema: %v", allowed)
				}
				// Simulate a backend ignoring that schema as well as valid names.
				body, _ := json.Marshal(Addressee{Kind: "address", Name: target, Bare: true})
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: string(body)}, Done: true})
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", cfg: &config.Config{OllamaNumCtx: 8192}}
			got, err := o.ResolveAddressee(t.Context(), AddressRequest{Participants: []GroupParticipant{{Name: "Obey"}, {Name: "Santa North", Aliases: []string{"Santa", " santa "}}}, Current: "Obey", Speaker: "Lance", Account: "user", Text: "Is there an uncensored LFM3 model?"})
			if err != nil {
				t.Fatal(err)
			}
			if target == "LFM3" || target == "Lance" {
				if got.Kind != "none" || got.Name != "" || got.Bare {
					t.Fatalf("unlisted decision=%+v", got)
				}
			} else if got.Kind != "address" || got.Name != target {
				t.Fatalf("configured target rejected: %+v", got)
			}
		})
	}
}
