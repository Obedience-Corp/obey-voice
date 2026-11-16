package brain

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// A warmup that reaches the runner after a turn's first request runs between
// that request and the next one, delaying the reply by a full prefill.
func TestWarmupAfterATurnStartedSendsNothing(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", fullSystemPrompt: "You are Samantha.",
		cfg: &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, MaxHistory: 10}}
	stream, err := o.ThinkStream(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	turn := count

	o.Warmup(t.Context())

	if count != turn {
		t.Fatalf("warmup after the first turn sent %d request(s)", count-turn)
	}
}

// A turn that starts while the first warmup request is in flight lets that
// request finish but stops the second one.
func TestWarmupStopsWhenATurnStartsDuringIt(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var warmups int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Options["num_predict"] == float64(1) {
			mu.Lock()
			warmups++
			first := warmups == 1
			mu.Unlock()
			if first {
				close(arrived)
				<-release
			}
		}
		if hasTool(req.Tools, "route") {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", fullSystemPrompt: "You are Samantha.",
		cfg: &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, MaxHistory: 10}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.Warmup(t.Context())
	}()
	<-arrived
	stream, err := o.ThinkStream(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	for range stream.Chunks {
	}
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if warmups != 1 {
		t.Fatalf("warmup requests = %d, want only the one already in flight", warmups)
	}
}
