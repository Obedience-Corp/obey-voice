package audiojob

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAudioTextRuntimeChecksCompleteInputAndTerminalResponse(t *testing.T) {
	for _, mode := range []string{"valid", "context", "count-mismatch", "length", "tools", "empty", "model", "runtime", "oversize", "truncated", "cancel", "no-input"} {
		t.Run(mode, func(t *testing.T) {
			var countCalls, generateCalls atomic.Int32
			wav := []byte("bounded WAV fixture")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing owned authentication")
					w.WriteHeader(401)
					return
				}
				var request struct {
					Model    string `json:"model"`
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
					MaxTokens int             `json:"max_tokens"`
					Cache     bool            `json:"cache_prompt"`
					Tools     json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if request.Model != ManagedModel || request.MaxTokens != MaxTextTokens || request.Cache || len(request.Tools) != 0 || len(request.Messages) != 2 {
					t.Error("unexpected inference policy")
					w.WriteHeader(400)
					return
				}
				var parts []struct {
					Type  string `json:"type"`
					Audio struct {
						Data string `json:"data"`
					} `json:"input_audio"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(request.Messages[1].Content, &parts); err != nil {
					t.Error(err)
					return
				}
				if len(parts) != 2 || parts[0].Audio.Data != base64.StdEncoding.EncodeToString(wav) || parts[1].Text != "Describe the sound." {
					t.Error("source or instruction changed")
					return
				}
				if r.URL.Path == "/v1/chat/completions/input_tokens" {
					countCalls.Add(1)
					tokens := 400
					if mode == "context" {
						tokens = ManagedContextTokens
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"object": "response.input_tokens", "input_tokens": tokens})
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					w.WriteHeader(404)
					return
				}
				generateCalls.Add(1)
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				prompt := 400
				finish := "stop"
				text := "A short draft."
				model, fingerprint := ManagedModel, ManagedVersion
				message := map[string]any{"role": "assistant", "content": text}
				switch mode {
				case "count-mismatch":
					prompt = 399
				case "length":
					finish = "length"
				case "tools":
					message["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": "delete_file"}}}
				case "empty":
					message["content"] = " "
				case "model":
					model = "other"
				case "runtime":
					fingerprint = "other"
				case "oversize":
					message["content"] = strings.Repeat("x", 129<<10)
				}
				response := map[string]any{"model": model, "system_fingerprint": fingerprint, "choices": []any{map[string]any{"finish_reason": finish, "message": message}}, "usage": TextUsage{PromptTokens: prompt, CompletionTokens: 4}}
				data, _ := json.Marshal(response)
				if mode == "truncated" {
					w.Header().Set("Content-Length", fmt.Sprint(len(data)+10))
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			runtime := &ManagedRuntime{endpoint: server.URL, token: "fixture-token", client: server.Client(), audioInput: mode != "no-input", textOutput: true}
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			text, _, err := runtime.Analyze(ctx, wav, "understanding", "Describe the sound.")
			if mode == "valid" {
				if err != nil || text != "A short draft." {
					t.Fatal(text, err)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if mode == "context" && generateCalls.Load() != 0 {
				t.Fatal("oversized input was dispatched")
			}
			if mode == "no-input" && (countCalls.Load() != 0 || generateCalls.Load() != 0) {
				t.Fatal("missing modality did not reject before dispatch")
			}
		})
	}
}
