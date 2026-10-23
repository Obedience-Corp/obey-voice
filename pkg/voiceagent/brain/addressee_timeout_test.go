package brain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// Model startup can outlast the former 15-second classifier budget even when
// generation itself is fast. Exercise the real request context, not a retry.
func TestResolveAddresseeWaitsForModelStartup(t *testing.T) {
	host, _ := url.Parse("http://localhost")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		timer := time.NewTimer(16 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":{"content":"{\"kind\":\"none\",\"name\":\"\",\"bare\":false}"},"done":true}`))}, nil
	})}
	o := &OllamaBrain{client: api.NewClient(host, client), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
	got, err := o.ResolveAddressee(t.Context(), AddressRequest{Account: "user", Current: "Jarvis", Text: "That sounds good."})
	if err != nil || got.Kind != "none" || calls != 1 {
		t.Fatalf("startup failed or request retried: decision=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestResolveAddresseeParentCancelsDuringStartup(t *testing.T) {
	host, _ := url.Parse("http://localhost")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	o := &OllamaBrain{client: api.NewClient(host, client), model: "test", cfg: &config.Config{OllamaNumCtx: 8192}}
	_, err := o.ResolveAddressee(ctx, AddressRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancel lost: %v", err)
	}
	if _, ok := admission.ClassifierFailureKindOf(err); ok {
		t.Fatalf("parent cancel became classifier failure: %v", err)
	}
}
