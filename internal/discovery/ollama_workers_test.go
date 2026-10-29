package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func workerCatalogServer(t *testing.T, models map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			var names []map[string]string
			for name := range models {
				names = append(names, map[string]string{"name": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": names})
		case "/api/show":
			var req struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			caps, exists := models[req.Model]
			if !exists || req.Model == "failed" {
				http.Error(w, "metadata unavailable", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
		default:
			t.Errorf("discovery attempted non-metadata request %s", r.URL.Path)
			http.Error(w, "unexpected request", 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaWorkerCatalogRequiresVerifiedCompletionAndTools(t *testing.T) {
	srv := workerCatalogServer(t, map[string][]string{
		"capable:latest": {"completion", "tools"}, "dolphin": {"completion"},
		"embedding": {"embedding"}, "tools-only": {"tools"}, "unknown": nil, "failed": nil,
	})
	p := discoverOllama(t.Context(), &config.Config{OllamaHost: srv.URL})
	if !p.Available || len(p.Models) != 6 {
		t.Fatalf("brain catalog was filtered: %+v", p)
	}
	if !reflect.DeepEqual(p.ForWorkers().Models, []string{"capable"}) || !p.ForWorkers().Available {
		t.Fatalf("incompatible or unknown worker models offered: %+v", p)
	}
}

func TestOllamaWorkersUseTheirOwnEndpoint(t *testing.T) {
	main := workerCatalogServer(t, map[string][]string{"dolphin": {"completion"}})
	worker := workerCatalogServer(t, map[string][]string{"worker-qwen": {"completion", "tools"}})
	for _, host := range []string{main.URL, "http://127.0.0.1:1"} {
		p := discoverOllama(t.Context(), &config.Config{OllamaHost: host, VoiceWorkerOllamaHost: worker.URL})
		if !p.ForWorkers().Available || !reflect.DeepEqual(p.ForWorkers().Models, []string{"worker-qwen"}) {
			t.Fatalf("worker discovery depended on the brain endpoint: %+v", p)
		}
	}
}

func TestOllamaCapabilityDiscoveryCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var shows atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"slow"}]}`))
			return
		}
		shows.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); srv.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan ProviderInfo, 1)
	go func() { done <- discoverOllama(ctx, &config.Config{OllamaHost: srv.URL}) }()
	<-started
	cancel()
	select {
	case p := <-done:
		if len(p.ForWorkers().Models) != 0 || shows.Load() != 1 {
			t.Fatalf("cancelled metadata granted tools: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("capability probes ignored cancellation")
	}
}
