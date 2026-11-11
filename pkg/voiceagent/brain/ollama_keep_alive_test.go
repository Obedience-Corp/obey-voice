package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestParseOllamaKeepAlive(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Duration
		isNil   bool
		wantErr bool
	}{
		{raw: "", isNil: true},
		{raw: "  ", isNil: true},
		{raw: "10m", want: 10 * time.Minute},
		{raw: "-1", want: -1},
		{raw: " -1 ", want: -1},
		{raw: "banana", isNil: true, wantErr: true},
	}
	for _, tc := range tests {
		got, err := parseOllamaKeepAlive(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
		if tc.isNil {
			if got != nil {
				t.Fatalf("%q: got %v, want nil", tc.raw, got)
			}
			continue
		}
		if got == nil || got.Duration != tc.want {
			t.Fatalf("%q: got %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestSkillRouterEmbedsSendForeverKeepAlive(t *testing.T) {
	resetSharedCatalogVectors(t)
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		n := 1
		if inputs, ok := body["input"].([]any); ok {
			n = len(inputs)
		}
		vectors := make([][]float32, n)
		for i := range vectors {
			vectors[i] = []float32{1}
		}
		_ = json.NewEncoder(w).Encode(api.EmbedResponse{Embeddings: vectors})
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	client := api.NewClient(base, srv.Client())

	cfg := &config.Config{OllamaKeepAlive: "-1"}
	keepAlive, err := parseOllamaKeepAlive(cfg.OllamaKeepAlive)
	if err != nil {
		t.Fatal(err)
	}
	router, err := newSharedSemanticSkillRouter(context.Background(), srv.URL, client, "embed", 0.55, routingCatalog(), keepAlive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Match(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("embed requests = %d, want 2", len(bodies))
	}
	for i, body := range bodies {
		if got := body["keep_alive"]; got != float64(-1) {
			t.Fatalf("request %d keep_alive = %v, want -1", i, got)
		}
	}
}
