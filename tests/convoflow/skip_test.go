package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tagsServer(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, `{"name":"`+n+`"}`)
		}
		_, _ = w.Write([]byte(`{"models":[` + strings.Join(parts, ",") + `]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeOllama(t *testing.T) {
	up := tagsServer(t, "qwen3:8b", "gemma3:latest")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	cases := []struct {
		name string
		k    knobs
		want string // substring of the reason; empty means no skip
	}{
		{"live and worker share one pulled model", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", ollamaHost: up.URL}, ""},
		{"untagged name matches :latest", knobs{liveProvider: "ollama", liveModel: "gemma3", workerProvider: "stub", ollamaHost: up.URL}, ""},
		{"live model not pulled", knobs{liveProvider: "ollama", liveModel: "qwen3:30b", workerProvider: "stub", ollamaHost: up.URL}, `live model "qwen3:30b" is not pulled`},
		{"distinct worker model not pulled", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", workerModel: "qwen3:30b", ollamaHost: up.URL}, `worker model "qwen3:30b" is not pulled`},
		{"daemon down", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", ollamaHost: down.URL}, "ollama not reachable"},
		{"stub worker needs no worker model", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "stub", workerModel: "nope", ollamaHost: up.URL}, ""},
		{"api worker skips the worker probe", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "grok", workerModel: "nope", ollamaHost: up.URL}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeOllama(context.Background(), tc.k)
			if tc.want == "" && got != "" {
				t.Fatalf("unexpected skip: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestSkipReasonGateRefusesStub(t *testing.T) {
	k := knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "stub", gate: true}
	if got := skipReason(k); !strings.Contains(got, "refuses stub") {
		t.Fatalf("skipReason = %q", got)
	}
	k.gate = false
	if got := skipReason(k); got != "" {
		t.Fatalf("run must allow stub: %q", got)
	}
}
