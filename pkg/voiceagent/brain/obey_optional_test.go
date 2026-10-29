package brain

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestObeyOptionalOtherProvidersWorkWithoutCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintln(w, `{"models":[{"name":"fixture:latest"}]}`)
		case "/api/chat":
			fmt.Fprintln(w, `{"model":"fixture:latest","message":{"role":"assistant","content":"Still working."},"done":true,"done_reason":"stop"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", MaxHistory: 6, PromptsDir: t.TempDir(), WorkDir: t.TempDir(), WorkDirSource: "fixture"}
	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.ThinkFull(t.Context(), "hello", StreamOptions{})
	if err != nil || text != "Still working." {
		t.Fatalf("reply=%q err=%v", text, err)
	}
	batch, err := NewBatchProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := batch.Transform(t.Context(), BatchRequest{Text: "source"})
	if err != nil || result.Text != "Still working." {
		t.Fatalf("batch=%+v err=%v", result, err)
	}
	cfg.BrainProvider = "obey"
	cfg.ObeyProvider = "fixture"
	cfg.ObeyModel = "fixture"
	cfg.ObeyToolPolicy = "daemon"
	if _, err := NewProvider(cfg); err == nil || !strings.Contains(err.Error(), "select another brain provider") {
		t.Fatalf("missing CLI error=%v", err)
	}
	if _, err := NewBatchProvider(cfg); err == nil || !strings.Contains(err.Error(), "select another brain provider") {
		t.Fatalf("missing batch CLI error=%v", err)
	}
}
