package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func showServer(t *testing.T, info map[string]any, shows *atomic.Int32) (*api.Client, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		shows.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"model_info": info})
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return api.NewClient(base, srv.Client()), srv.URL
}

func TestPromptBatchRaisedForSlidingWindowModels(t *testing.T) {
	sliding := map[string]any{"general.architecture": "gemma4", "gemma4.attention.sliding_window": 1024}
	plain := map[string]any{"general.architecture": "qwen3", "qwen3.attention.head_count": 40}
	tests := []struct {
		name string
		info map[string]any
		cfg  config.Config
		want int
	}{
		{name: "sliding window default batch", info: sliding, cfg: config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 128}, want: 512},
		{name: "full attention keeps batch", info: plain, cfg: config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 128}, want: 128},
		{name: "chosen batch is sent as written", info: sliding, cfg: config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 128, OllamaNumBatchExplicit: true}, want: 128},
		{name: "server choice is left alone", info: sliding, cfg: config.Config{OllamaNumCtx: 32768}, want: 0},
		{name: "larger batch is kept", info: sliding, cfg: config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 1024}, want: 1024},
		{name: "window smaller than the raised batch", info: sliding, cfg: config.Config{OllamaNumCtx: 256, OllamaNumBatch: 128}, want: 256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var shows atomic.Int32
			client, host := showServer(t, tt.info, &shows)
			tt.cfg.OllamaHost = host
			if got := promptBatch(client, "m", &tt.cfg); got != tt.want {
				t.Fatalf("promptBatch = %d, want %d", got, tt.want)
			}
			if got := promptBatch(client, "m", &tt.cfg); got != tt.want {
				t.Fatalf("second promptBatch = %d, want %d", got, tt.want)
			}
			if shows.Load() > 1 {
				t.Fatalf("model looked up %d times, want at most once", shows.Load())
			}
		})
	}
}

func TestPromptBatchKeepsConfiguredBatchWhenLookupFails(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	cfg := &config.Config{OllamaHost: srv.URL, OllamaNumCtx: 8192, OllamaNumBatch: 128}
	if got := promptBatch(api.NewClient(base, srv.Client()), "m", cfg); got != 128 {
		t.Fatalf("promptBatch = %d, want the configured 128", got)
	}
}

// The request, its warmup and the skill-confirmation call must agree, or
// Ollama reloads the model between them.
func TestChatRequestCarriesSlidingWindowBatch(t *testing.T) {
	var shows atomic.Int32
	client, host := showServer(t, map[string]any{"gemma4.attention.sliding_window": 1024}, &shows)
	o := &OllamaBrain{client: client, modelLookup: true, model: "gemma", cfg: &config.Config{OllamaHost: host, OllamaNumCtx: 32768, OllamaNumBatch: 128}}
	if got := o.newChatRequest(nil, nil).Options["num_batch"]; got != 512 {
		t.Fatalf("chat num_batch = %v, want 512", got)
	}
	if got := o.requestOptionsFor(0)["num_batch"]; got != 512 {
		t.Fatalf("warmup num_batch = %v, want 512", got)
	}
}
