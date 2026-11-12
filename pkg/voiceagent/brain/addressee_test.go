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
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestResolveAddresseePrivateAndValidated(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        bool
		kind       admission.ClassifierFailureKind
	}{
		{"address", `{"kind":"address","name":"Santa","bare":false}`, false, ""},
		{"none", `{"kind":"none","name":"","bare":false}`, false, ""},
		{"greeting without addressee", `{"kind":"none","name":"","bare":true}`, false, ""},
		{"ambiguous", `{"kind":"ambiguous","name":"Santa","bare":false}`, false, ""},
		{"malformed", `not json`, true, admission.ClassifierInvalidOutput},
		{"unknown kind", `{"kind":"switch","name":"Santa","bare":false}`, true, admission.ClassifierInvalidOutput},
		{"none with a mentioned name", `{"kind":"none","name":"Santa","bare":true}`, false, ""},
		{"empty", `{"kind":"address","name":"","bare":false}`, true, admission.ClassifierInvalidOutput},
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
			if !tc.bad && result.Kind == "none" && result.Bare {
				t.Fatal("no-address greeting must remain a full conversational turn")
			}
			if tc.kind != "" {
				got, ok := admission.ClassifierFailureKindOf(err)
				if !ok || got != tc.kind {
					t.Fatalf("classifier kind=%q ok=%v err=%v", got, ok, err)
				}
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
	if _, ok := admission.ClassifierFailureKindOf(err); ok {
		t.Fatalf("parent cancellation misclassified as classifier failure: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveAddresseeTransportFailureClassification(t *testing.T) {
	host, _ := url.Parse("http://localhost")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	o := &OllamaBrain{client: api.NewClient(host, client), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
	_, err := o.ResolveAddressee(t.Context(), AddressRequest{})
	if got, ok := admission.ClassifierFailureKindOf(err); !ok || got != admission.ClassifierUnavailable {
		t.Fatalf("kind=%q ok=%v err=%v", got, ok, err)
	}
}

func TestResolveAddresseeLocalTimeoutClassification(t *testing.T) {
	host, _ := url.Parse("http://localhost")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	o := &OllamaBrain{client: api.NewClient(host, client), model: "test", cfg: &config.Config{OllamaNumCtx: 8192, PersonaRoutingTimeout: 1}}
	_, err := o.ResolveAddressee(t.Context(), AddressRequest{})
	if got, ok := admission.ClassifierFailureKindOf(err); !ok || got != admission.ClassifierLocalTimeout {
		t.Fatalf("kind=%q ok=%v err=%v", got, ok, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
}

func TestResolveAddresseeParentDeadlineNotClassifiedAsLocalTimeout(t *testing.T) {
	host, _ := url.Parse("http://localhost")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	o := &OllamaBrain{client: api.NewClient(host, client), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := o.ResolveAddressee(ctx, AddressRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
	if _, ok := admission.ClassifierFailureKindOf(err); ok {
		t.Fatalf("parent deadline misclassified as local classifier timeout: %v", err)
	}
}
