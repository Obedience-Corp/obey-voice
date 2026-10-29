package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func ollamaConstructorConfig(t *testing.T, host string) *config.Config {
	t.Helper()
	// Give workspace discovery a deterministic home boundary and use an
	// empty temporary directory so the catalog contains only the fixture
	// and shipped skills; return one embedding per requested description.
	fakeHome := t.TempDir()
	restoreHome := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restoreHome)
	skillDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(skillDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: A constructor test skill.\n---\nUse the demo skill.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		OllamaHost:           host,
		OllamaModel:          "chat-model",
		OllamaEmbeddingModel: "embed-model",
		SkillsEnabled:        true,
		SkillsDir:            skillDir,
		WorkDir:              t.TempDir(),
		VoiceToolsEnabled:    false,
	}
}

// The probe has a five second budget. Catalog loading must not inherit the
// probe's nearly expired context, and embedding must receive its own ten
// second budget from the caller. A list response taking four seconds followed
// by a six second embedding therefore still constructs successfully.
func TestNewOllamaSlowProbeDoesNotConsumeEmbeddingBudget(t *testing.T) {
	listStarted := make(chan struct{})
	embedStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			close(listStarted)
			time.Sleep(4 * time.Second)
			_, _ = io.WriteString(w, `{"models":[{"name":"chat-model"}]}`)
		case "/api/embed":
			close(embedStarted)
			time.Sleep(6 * time.Second)
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			embeddings := make([][]float32, len(request.Input))
			for i := range embeddings {
				embeddings[i] = []float32{1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	brain, err := newOllama(ctx, ollamaConstructorConfig(t, srv.URL))
	if err != nil {
		t.Fatalf("newOllama() error after slow probe and embedding: %v", err)
	}
	if brain.skillRouter == nil {
		t.Fatal("newOllama() did not build semantic skill router")
	}
	select {
	case <-listStarted:
	case <-time.After(time.Second):
		t.Fatal("probe did not reach Ollama fixture")
	}
	select {
	case <-embedStarted:
	case <-time.After(time.Second):
		t.Fatal("embedding did not reach Ollama fixture")
	}
}

func TestNewOllamaParentCancellationDuringEmbedding(t *testing.T) {
	embedStarted := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"chat-model"}]}`)
		case "/api/embed":
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				return
			}
			close(embedStarted)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	cfg := ollamaConstructorConfig(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := newOllama(ctx, cfg); done <- err }()
	select {
	case <-embedStarted:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("embedding did not reach fixture")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("constructor cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("constructor did not honor cancellation")
	}
}
