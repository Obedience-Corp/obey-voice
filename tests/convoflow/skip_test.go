package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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

// A scenario may pin the worker (or the live brain) to another model or
// host. The probe must look there, not at the inherited live pair, or the
// run reaches an unprobed target and fails late instead of exiting 2.
func TestProbeOllamaUsesScenarioOverrides(t *testing.T) {
	live := tagsServer(t, "qwen3:8b")
	other := tagsServer(t, "qwen3:30b")
	cli := knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", ollamaHost: live.URL}

	cases := []struct {
		name string
		yaml string
		k    knobs
		want string
	}{
		{"worker model override not pulled", "name: t\nworker: { model: qwen3:30b }\n", cli, `worker model "qwen3:30b" is not pulled on ` + live.URL},
		{"worker host override is probed", "name: t\nworker: { model: qwen3:30b, host: " + other.URL + " }\n", cli, ""},
		{"worker host override without the model", "name: t\nworker: { model: qwen3:8b, host: " + other.URL + " }\n", cli, `worker model "qwen3:8b" is not pulled on ` + other.URL},
		{"live model override not pulled", "name: t\nbrain: { model: qwen3:30b }\n", knobs{liveProvider: "ollama", workerProvider: "stub", ollamaHost: live.URL}, `live model "qwen3:30b" is not pulled`},
		{"live host override is probed", "name: t\nbrain: { model: qwen3:30b, host: " + other.URL + " }\n", knobs{liveProvider: "ollama", workerProvider: "stub"}, ""},
		{"CLI worker model wins over the scenario", "name: t\nworker: { model: qwen3:30b }\n", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", workerModel: "qwen3:8b", ollamaHost: live.URL}, ""},
		{"stub ignores the scenario worker", "name: t\nworker: { provider: ollama, model: qwen3:30b }\n", knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "stub", ollamaHost: live.URL}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := loadScenarioString("t", tc.yaml)
			if err != nil {
				t.Fatal(err)
			}
			eff := tc.k.withScenario(scn)
			got := probeOllama(context.Background(), eff)
			if tc.want == "" && got != "" {
				t.Fatalf("unexpected skip: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The probe and resolveConfig must agree on what the run uses.
func TestProbeAndResolveConfigSeeTheSameWorker(t *testing.T) {
	scn, err := loadScenarioString("t", "name: t\nworker: { model: qwen3:30b, host: http://other:11434 }\n")
	if err != nil {
		t.Fatal(err)
	}
	k := knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", ollamaHost: "http://live:11434"}
	eff := k.withScenario(scn)
	if eff.resolvedWorkerModel() != "qwen3:30b" || eff.resolvedWorkerHost() != "http://other:11434" {
		t.Fatalf("effective worker = %s @ %s", eff.resolvedWorkerModel(), eff.resolvedWorkerHost())
	}
	cfg, prof, err := resolveConfig(&config.Config{}, scn, k, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoiceWorkerModel != eff.resolvedWorkerModel() || cfg.VoiceWorkerOllamaHost != eff.resolvedWorkerHost() {
		t.Fatalf("config worker = %s @ %s, probe saw %s @ %s", cfg.VoiceWorkerModel, cfg.VoiceWorkerOllamaHost, eff.resolvedWorkerModel(), eff.resolvedWorkerHost())
	}
	if prof.WorkerModel != "qwen3:30b" || prof.WorkerHost != "http://other:11434" {
		t.Fatalf("profile = %+v", prof)
	}
}
