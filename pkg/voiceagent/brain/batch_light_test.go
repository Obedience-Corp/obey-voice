package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func lightStub(t *testing.T, models []string, reply string, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		list := make([]map[string]any, 0, len(models))
		for _, m := range models {
			list = append(list, map[string]any{"name": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*bodies = append(*bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]any{"role": "assistant", "content": reply}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNewOllamaBatchLight(t *testing.T) {
	var bodies []map[string]any
	srv := lightStub(t, []string{"small:latest"}, "cleaned", &bodies)

	tests := []struct {
		name    string
		cfg     config.Config
		wantErr string
	}{
		{"no model", config.Config{OllamaHost: srv.URL}, "ollama_model not configured"},
		{"bad host", config.Config{OllamaHost: "::nope", OllamaModel: "small"}, "invalid ollama_host"},
		{"server down", config.Config{OllamaHost: "http://127.0.0.1:9", OllamaModel: "small"}, "cannot connect to ollama"},
		{"model missing", config.Config{OllamaHost: srv.URL, OllamaModel: "other"}, "ollama pull other"},
		{"ok", config.Config{OllamaHost: srv.URL, OllamaModel: "small", OllamaKeepAlive: "-1", OllamaThink: "off", OllamaNumCtx: 2048}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bp, err := NewOllamaBatchLight(&tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			res, err := bp.Transform(context.Background(), BatchRequest{SystemPrompt: "sys", Text: "hello"})
			if err != nil || res.Text != "cleaned" || res.Model != "small" {
				t.Fatalf("res = %+v, err %v", res, err)
			}
			body := bodies[len(bodies)-1]
			opts := body["options"].(map[string]any)
			if body["think"] != false || body["keep_alive"] == nil || opts["num_ctx"] != float64(2048) || opts["temperature"] != float64(0) {
				t.Fatalf("request = %v", body)
			}
		})
	}
}

// Swaps package globals; must not run in parallel.
func TestNewOllamaBatchLightWarnsOnInvalidKeepAlive(t *testing.T) {
	var buf strings.Builder
	prev := keepAliveWarnOut
	keepAliveWarnOut = &buf
	keepAliveWarned.Clear()
	t.Cleanup(func() { keepAliveWarnOut = prev; keepAliveWarned.Clear() })

	var bodies []map[string]any
	srv := lightStub(t, []string{"small:latest"}, "cleaned", &bodies)
	bp, err := NewOllamaBatchLight(&config.Config{OllamaHost: srv.URL, OllamaModel: "small", OllamaKeepAlive: "forever"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bp.Transform(context.Background(), BatchRequest{SystemPrompt: "sys", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `invalid ollama_keep_alive "forever"`) {
		t.Fatalf("no warning for the light batch path: %q", buf.String())
	}
	if len(bodies) == 0 {
		t.Fatal("no chat request reached the server")
	}
	if got, sent := bodies[len(bodies)-1]["keep_alive"]; sent {
		t.Fatalf("invalid keep_alive sent as %v, want the field omitted", got)
	}
}
