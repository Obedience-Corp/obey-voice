package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestLegacyWorkerWithoutToolsCannotStartInference(t *testing.T) {
	var inference atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"chat-only"}]}`))
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion"],"details":{"family":"fixture"}}`))
		default:
			inference.Add(1)
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"invented result"},"done":true}`))
		}
	}))
	defer srv.Close()
	cfg := &config.Config{OllamaHost: srv.URL, OllamaModel: "chat-only", VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true, WorkDir: t.TempDir(), PromptsDir: t.TempDir(), SkillsDir: t.TempDir(), MaxHistory: 10}
	worker, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{}, jobs, worker, cfg, nil)
	// The optional work inbox is off. Saved/inherited legacy workers still
	// require verified tools, rather than silently retrying as plain chat.
	result, err := a.workerRun("look up the weather", worker, cfg)(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tool-calling") || result != "" || inference.Load() != 0 {
		t.Fatalf("incompatible worker ran: result=%q error=%v inference=%d", result, err, inference.Load())
	}
}
