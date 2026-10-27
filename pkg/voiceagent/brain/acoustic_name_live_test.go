//go:build integration

package brain

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// Synthetic transcript replay through the real private classifier. This is not
// a microphone test or proof of accuracy on other models/speakers.
func TestAcousticNameLiveRouting(t *testing.T) {
	model := os.Getenv("OBEY_KWS_ROUTER_MODEL")
	if model == "" {
		t.Skip("set OBEY_KWS_ROUTER_MODEL to an installed local Ollama model")
	}
	host, _ := url.Parse("http://127.0.0.1:11434")
	o := &OllamaBrain{client: api.NewClient(host, http.DefaultClient), model: model, cfg: &config.Config{OllamaNumCtx: 8192, PersonaRoutingTimeout: 120}}
	for _, tc := range []struct{ text, name, kind string }{
		{"Oh bay, can you help me?", "Obey", "address"},
		{"Heyobe, tell me the weather.", "Obey", "address"},
		{"Hey, oh bay, tell me the weather.", "Obey", "address"},
		{"You must obey the rules.", "Obey", "none"},
		{"Tell me about Obey.", "Obey", "none"},
		{"He said: Obey, can you help me?", "Obey", "none"},
		{"Okay, tell me the weather.", "", "none"},
		{"Oh baby, what a beautiful day.", "Obey", "none"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			req := AddressRequest{Participants: []GroupParticipant{{Name: "Jarvis"}, {Name: "Obey"}}, Current: "Jarvis", Account: "user", Speaker: "Unknown", Text: tc.text, AcousticName: tc.name}
			if tc.kind == "address" {
				baseline := req
				baseline.AcousticName = ""
				before, err := o.ResolveAddressee(t.Context(), baseline)
				t.Logf("without hint=%+v err=%v", before, err)
			}
			got, err := o.ResolveAddressee(t.Context(), req)
			t.Logf("model=%s result=%+v err=%v", model, got, err)
			if err != nil || got.Kind != tc.kind || (tc.kind == "address" && (got.Name != "Obey" || got.Bare)) {
				t.Fatalf("want=%s got=%+v err=%v", tc.kind, got, err)
			}
		})
	}
}
