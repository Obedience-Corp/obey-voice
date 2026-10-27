package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestRoutingCancellationDropsUndecidedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"message":{"content":"This may be a routing plan."}}`+"\n")
		// A separate frame proves the preceding content callback has run.
		_, _ = io.WriteString(w, `{"message":{"thinking":"barrier"}}`+"\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := o.ThinkStream(ctx, "help", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-stream.Chunks:
		t.Fatalf("undecided text escaped: %q", text)
	case <-stream.Thinking:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	text, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err == nil || text != "" {
		t.Fatalf("canceled routing = %q %+v", text, result)
	}
}

func TestAnswerAfterChatRouteStillStreamsBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Tools) > 0 {
			_, _ = io.WriteString(w, `{"message":{"tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"message":{"content":"Hello there. "}}`+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		_, _ = io.WriteString(w, `{"message":{"content":"How can I help?"},"done":true}`+"\n")
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	stream, err := o.ThinkStream(ctx, "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	select {
	case first = <-stream.Chunks:
	case <-ctx.Done():
		t.Fatal("answer was held until request completion")
	}
	close(release)
	rest, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil || first+rest != "Hello there. How can I help?" {
		t.Fatalf("answer = %q %+v", first+rest, result)
	}
}

func TestLiveRoutingPreambleIsNeverSpokenOnHandoffOrFailure(t *testing.T) {
	for _, ending := range []string{"spawn", "narrate", "error", "incomplete"} {
		t.Run(ending, func(t *testing.T) {
			lines := []string{`{"message":{"role":"assistant","content":"I need to consider how to handle this request."}}`}
			if ending == "error" {
				lines = append(lines, `{"error":"fixture disconnected"}`)
			} else if ending != "incomplete" {
				lines = append(lines, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"`+ending+`"}}}]},"done":true}`)
			}
			o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
			stream, err := o.ThinkStream(t.Context(), "help", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
			if err != nil {
				t.Fatal(err)
			}
			text, _ := drainStream(t, stream)
			result := <-stream.Done
			if strings.Contains(text, "consider") {
				t.Fatalf("planning spoken: %q", text)
			}
			if ending == "error" || ending == "incomplete" {
				if text != "" || result.Err == nil {
					t.Fatalf("missing recovery: %q %+v", text, result)
				}
			} else if text != "" || result.Route != ending {
				t.Fatalf("handoff = %q %+v", text, result)
			}
		})
	}
}

func TestBlockingChatRouteIsChosenOnlyOnce(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		if len(req.Tools) > 0 {
			// A router that keeps choosing chat whenever we offer route.
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Private routing plan.","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
			return
		}
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Private routing plan") {
				t.Error("planning retained in history")
			}
		}
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Hello there."},"done":true}`+"\n")
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	reply, err := o.ThinkFull(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil || reply != "Hello there." || requests != 2 {
		t.Fatalf("reply=%q requests=%d err=%v", reply, requests, err)
	}
}

func TestChatAnswerCannotExecuteAnotherRouteWithoutTools(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "streaming"}[streaming], func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				// Deliberately ignore the tools-disabled answer request.
				_, _ = io.WriteString(w, `{"message":{"tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
			}))
			defer srv.Close()
			base, _ := url.Parse(srv.URL)
			o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			if streaming {
				stream, err := o.ThinkStream(t.Context(), "hello", opts)
				if err != nil {
					t.Fatal(err)
				}
				text, _ := drainStream(t, stream)
				if result := <-stream.Done; result.Err == nil || !result.Recovered || strings.Contains(text, "circles") {
					t.Fatalf("missing bounded recovery: %q %+v", text, result)
				}
			} else if _, err := o.ThinkFull(t.Context(), "hello", opts); err == nil {
				t.Fatal("accepted an unexpected route")
			}
			if requests != 2 {
				t.Fatalf("routing loop made %d requests", requests)
			}
		})
	}
}
