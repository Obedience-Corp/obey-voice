//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// phononOllama is an Ollama that reports version 0.40.9 and lists tags; any
// pull is counted and answered with success.
func phononOllama(t *testing.T, tags ...string) (*config.Config, *atomic.Int32) {
	t.Helper()
	var pulls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"0.40.9"}`)
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		models := []map[string]string{}
		for _, n := range tags {
			models = append(models, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/api/pull", func(w http.ResponseWriter, _ *http.Request) {
		pulls.Add(1)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechURL: srv.URL + "/v1", ModelsDir: t.TempDir()}, &pulls
}

func placeVoicesFile(t *testing.T, cfg *config.Config) {
	t.Helper()
	path := filepath.Join(cfg.ModelsDir, filepath.FromSlash(config.OpenAISpeechVoicesFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"name":"Zoey"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModelsStatusReportsPhononAndItsCatalog(t *testing.T) {
	cfg, _ := phononOllama(t, "phonon:latest")
	out := runStatusScoped(t, cfg, scopeFlags{tts: true}, cfg.ModelsDir, true)
	var statuses []config.AssetStatus
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	byID := statusByID(statuses)
	if model := byID[config.OpenAISpeechModelAssetID]; !model.Installed || model.Provider != config.OpenAISpeechProviderName {
		t.Fatalf("model row = %+v", model)
	}
	if voices := byID[config.OpenAISpeechVoicesAssetID]; voices.Installed {
		t.Fatalf("catalog row = %+v, want missing", voices)
	}

	human := runStatusScoped(t, cfg, scopeFlags{tts: true}, cfg.ModelsDir, false)
	if !strings.Contains(human, "phonon (Ollama) (openai-speech/ollama) — installed") || !strings.Contains(human, "speech works without it") {
		t.Fatalf("human status:\n%s", human)
	}
}

func TestModelsEnsureJSONSkipsAnInstalledPhonon(t *testing.T) {
	stubPersonas(t, nil, nil)
	cfg, pulls := phononOllama(t, "phonon:latest")
	placeVoicesFile(t, cfg)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	if err := runModelsEnsureJSON(cmd, cfg, scopeFlags{tts: true}.request(cfg), ""); err != nil {
		t.Fatalf("ensure: %v\n%s", err, buf.String())
	}
	if pulls.Load() != 0 {
		t.Fatalf("pulled %d times with phonon listed", pulls.Load())
	}
	lines := decodeNDJSONLines(t, buf.String())
	last := lines[len(lines)-1]
	skipped, _ := last["skipped"].([]any)
	var names []string
	for _, s := range skipped {
		names = append(names, fmt.Sprint(s))
	}
	got := strings.Join(names, ",")
	if last["done"] != true || !strings.Contains(got, "phonon (Ollama)") || !strings.Contains(got, config.OpenAISpeechVoicesFile) {
		t.Fatalf("terminal line = %v", last)
	}
}

func TestVoicesGateBlocksOnlyOnTheModel(t *testing.T) {
	cfg, _ := phononOllama(t)
	if got := missingTTSAssets(context.Background(), cfg); len(got) != 1 || got[0] != "phonon (Ollama)" {
		t.Fatalf("missing with phonon not pulled = %v", got)
	}

	cfg, _ = phononOllama(t, "phonon:latest")
	if got := missingTTSAssets(context.Background(), cfg); len(got) != 0 {
		t.Fatalf("missing with phonon pulled and no voices.jsonl = %v, want none", got)
	}
}
