//go:build integration

package brain

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestLiveGroupAddressee(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_ADDRESS_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_ADDRESS_MODEL to an installed Ollama model")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434"
	}
	base, err := url.Parse(host)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: model, cfg: &config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128}}
	for _, tc := range []struct{ text, account, want string }{
		{"Santa, what do you think about that?", "user", "Santa"},
		{"What do you think about that Santa", "user", "Santa"},
		{"You there Jarvis", "user", "Jarvis"},
		{"I'd like to hear Santa's take on this.", "user", "Santa"},
		{"Jarvis, ask Santa what she thinks about our code. Ask her directly by name and let her answer.", "user", "Jarvis"},
		{"Jarvis could you find out whether Santa agrees?", "user", "Jarvis"},
		{"Santa is funny.", "user", ""},
		{"What do you think about Santa?", "user", ""},
		{`He said 'Hey Santa, what do you think?' yesterday.`, "user", ""},
		{"Yeah", "user", ""},
		{"Jarvis, how would you approach it?", "agent", "Jarvis"},
		{"Jarvis gave me a useful answer earlier.", "agent", ""},
		{"The user could ask Santa about it later.", "agent", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := o.ResolveAddressee(t.Context(), AddressRequest{Participants: []GroupParticipant{{Name: "Jarvis"}, {Name: "Santa"}}, Current: "Santa", Speaker: map[string]string{"user": "Lance", "agent": "Santa"}[tc.account], Account: tc.account, Text: tc.text})
			if err != nil {
				t.Fatal(err)
			}
			wantKind := "address"
			if tc.want == "" {
				wantKind = "none"
			}
			if got.Kind != wantKind || got.Name != tc.want {
				t.Fatalf("decision=%+v want=%s %s", got, wantKind, tc.want)
			}
		})
	}
}
