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

// phononOllama is an Ollama that reports version 0.40.11 and lists tags; any
// pull is counted and answered with success.
func phononOllama(t *testing.T, tags ...string) (*config.Config, *atomic.Int32) {
	t.Helper()
	var pulls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"0.40.11"}`)
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		models := []map[string]string{}
		for _, n := range tags {
			models = append(models, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"voices":["default","Zoey"]}`) })
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
	if voices := byID[config.OpenAISpeechVoicesAssetID]; voices.Installed || !voices.Optional {
		t.Fatalf("catalog row = %+v, want missing and optional", voices)
	}
	if !strings.Contains(out, `"optional": true`) {
		t.Fatalf("status JSON does not carry optional:\n%s", out)
	}

	// The catalog file is listed, never counted: a Phonon user without it is
	// not told their install is incomplete. The two Qwen tier rows every TTS
	// status lists are the other optional rows.
	human := runStatusScoped(t, cfg, scopeFlags{tts: true}, cfg.ModelsDir, false)
	for _, want := range []string{
		"phonon (Ollama) (openai-speech/ollama) — installed",
		"speech works without it",
		"4 asset(s), 0 missing (plus 3 optional asset(s) not installed).",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("human status lacks %q:\n%s", want, human)
		}
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
	if err := runModelsEnsureJSON(cmd, cfg, scopeFlags{tts: true}.ensureRequest(cfg), ""); err != nil {
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

func TestVoicesGateIgnoresAListingThatFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"version":"0.40.11"}`) })
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechURL: srv.URL + "/v1", ModelsDir: t.TempDir()}
	if got := missingTTSAssets(context.Background(), cfg); len(got) != 0 {
		t.Fatalf("missing = %v, want none: an unknown listing is not a missing model", got)
	}
}

func TestModelsEnsureJSONVerifiesPhononBeforeSuccess(t *testing.T) {
	stubPersonas(t, nil, nil)
	for _, readyAfterPull := range []bool{true, false} {
		t.Run(fmt.Sprint(readyAfterPull), func(t *testing.T) {
			var pulls atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"version":"0.40.11"}`) })
			mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"models":[{"name":"phonon:latest"}]}`) })
			mux.HandleFunc("/api/show", func(w http.ResponseWriter, _ *http.Request) {
				if readyAfterPull && pulls.Load() > 0 {
					fmt.Fprint(w, `{"voices":["default","Zoey"]}`)
				} else {
					fmt.Fprint(w, `{"voices":["default"]}`)
				}
			})
			mux.HandleFunc("/api/pull", func(w http.ResponseWriter, _ *http.Request) {
				pulls.Add(1)
				fmt.Fprintln(w, `{"status":"success"}`)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			cfg := &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechURL: srv.URL + "/v1", ModelsDir: t.TempDir()}
			placeVoicesFile(t, cfg)
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetContext(context.Background())
			err := runModelsEnsureJSON(cmd, cfg, scopeFlags{tts: true}.ensureRequest(cfg), "")
			lines := decodeNDJSONLines(t, out.String())
			last := lines[len(lines)-1]
			if pulls.Load() != 1 || (err == nil) != readyAfterPull || last["done"] != readyAfterPull {
				t.Fatalf("pulls=%d error=%v output=%s", pulls.Load(), err, out.String())
			}
			if !readyAfterPull && (last["asset"] != "phonon (Ollama)" || !strings.Contains(fmt.Sprint(last["error"]), "still lists no named voices")) {
				t.Fatalf("failed verification not attributed to the model: %v", last)
			}
		})
	}
}
