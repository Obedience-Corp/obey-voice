package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/ollama/ollama/api"
)

func TestSystemYAMLEditReachesNextBlockingAndStreamingRequests(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "prompts")
	if _, err := prompts.Seed(dir); err != nil {
		t.Fatal(err)
	}
	writeApp := func(marker string) {
		t.Helper()
		body := "schema: obey-voice.prompt.v1\nprompt:\n  name: application\n  kind: system\n  system_prompt: " + marker + "\n"
		if err := os.WriteFile(filepath.Join(dir, "system", "application.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var systems []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var request api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		systems = append(systems, request.Messages[0].Content)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Ready."}, Done: true})
	}))
	defer server.Close()
	writeApp("APP_EDIT_A")
	b, err := NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", OllamaNumCtx: 8192,
		AgentName: "Obey", Persona: "obey", WorkDir: root, PromptsDir: dir, MaxHistory: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ThinkFull(t.Context(), "First question", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	writeApp("APP_EDIT_B")
	stream, err := b.ThinkStream(t.Context(), "Next question", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := b.ThinkFull(t.Context(), "Same settings", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(systems) != 3 || !strings.Contains(systems[0], "APP_EDIT_A") || !strings.Contains(systems[1], "APP_EDIT_B") || strings.Contains(systems[1], "APP_EDIT_A") {
		t.Fatalf("YAML edit did not reach the next request: %v", systems)
	}
	if systems[1] != systems[2] {
		t.Fatal("unchanged YAML changed the system prefix")
	}
	for _, turn := range b.History() {
		if strings.Contains(turn.Content, "APP_EDIT_") {
			t.Fatal("system template leaked into canonical conversation text")
		}
	}
}
