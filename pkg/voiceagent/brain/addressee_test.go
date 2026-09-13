package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestResolveAddresseePrivateAndValidated(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"address", `{"kind":"address","name":"Santa","bare":false}`, false},
		{"none", `{"kind":"none","name":"","bare":false}`, false},
		{"ambiguous", `{"kind":"ambiguous","name":"Santa","bare":false}`, false},
		{"malformed", `not json`, true},
		{"unknown kind", `{"kind":"switch","name":"Santa","bare":false}`, true},
		{"contradictory", `{"kind":"none","name":"Santa","bare":true}`, true},
		{"empty", `{"kind":"address","name":"","bare":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire api.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: tc.body}, Done: true})
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
			o.LoadHistory([]Turn{{Role: "user", Content: "remember cobalt"}})
			before := o.History()
			result, err := o.ResolveAddressee(t.Context(), AddressRequest{Participants: []GroupParticipant{{Name: "Jarvis"}, {Name: "Santa"}}, Account: "user", Text: "What do you think Santa"})
			if (err != nil) != tc.bad {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !reflect.DeepEqual(before, o.History()) {
				t.Fatal("classifier changed history")
			}
			if len(wire.Tools) != 0 || wire.Stream == nil || *wire.Stream || len(wire.Format) == 0 || wire.Think == nil || wire.Think.Value != false {
				t.Fatalf("not private structured request: %+v", wire)
			}
			if strings.Contains(wire.Messages[1].Content, "remember cobalt") {
				t.Fatal("implicitly sent full provider history")
			}
		})
	}
}

func TestResolveAddresseeCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	host, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(host, server.Client())}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := o.ResolveAddressee(ctx, AddressRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
