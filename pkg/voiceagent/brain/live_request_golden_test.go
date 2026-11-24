package brain

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// liveRequestServer records the body of every chat request, byte for byte,
// and answers each with a short reply.
type liveRequestServer struct {
	mu     sync.Mutex
	bodies [][]byte
	reply  func(n int) api.Message
}

func (s *liveRequestServer) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

func newLiveRequestBrain(t *testing.T, cfg *config.Config, reply func(n int) api.Message) (*OllamaBrain, *liveRequestServer) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	rec := &liveRequestServer{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, body)
		n := len(rec.bodies)
		rec.mu.Unlock()
		msg := api.Message{Role: "assistant", Content: "Hello."}
		if rec.reply != nil {
			msg = rec.reply(n)
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
	}))
	t.Cleanup(srv.Close)
	cfg.OllamaHost = srv.URL
	o, err := NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o, rec
}

func liveRequestConfig(t *testing.T) *config.Config {
	return &config.Config{
		OllamaModel:       "fixture",
		AgentName:         "Robin",
		ActivePersona:     "robin",
		PromptsDir:        t.TempDir(),
		WorkDir:           t.TempDir(),
		MaxHistory:        20,
		VoiceToolsEnabled: true,
		VoiceRouterTools:  "direct",
	}
}

// liveDirectOptions are the options the host gives an ordinary spoken turn
// on the direct router with a background worker available.
func liveDirectOptions(facts []WorkerFact) StreamOptions {
	return StreamOptions{
		WorkerFacts:       facts,
		Identity:          &TurnIdentity{PersonaID: "robin", PersonaDisplayName: "Robin", ResponseID: "response-1"},
		VoiceMode:         true,
		ToolsEnabled:      true,
		RouterToolsPolicy: RouterToolsDirect,
		BackgroundWorker:  true,
	}
}

func goldenJobs() []WorkerFact {
	return []WorkerFact{
		{ID: "job-1", Tier: "legacy", State: "running", Task: "Summarize the quarterly notes", Running: 4 * time.Minute, Progress: "read 3 files, ran 2 commands", Own: true},
		{ID: "job-2", Tier: "legacy", State: "queued", Task: "Download the release archive", Own: true},
		{ID: "job-3", Tier: "legacy", State: "running", Task: "Render the demo video", Running: 9 * time.Minute, Starter: "Sage"},
	}
}

// liveDirectRequests are the bytes an ordinary direct turn sends, without
// and with jobs, and the warm-up request before the first turn.
func liveDirectRequests(t *testing.T, mutate func(*config.Config, *StreamOptions)) string {
	t.Helper()
	var out []string
	for _, facts := range [][]WorkerFact{nil, goldenJobs()} {
		cfg := liveRequestConfig(t)
		opts := liveDirectOptions(facts)
		if mutate != nil {
			mutate(cfg, &opts)
		}
		o, rec := newLiveRequestBrain(t, cfg, nil)
		turn := runRoutedTurn(t, o, "How is it going?", opts)
		if turn.result.Err != nil {
			t.Fatal(turn.result.Err)
		}
		out = append(out, normalizeLiveRequest(cfg, rec.requests())...)
	}
	cfg := liveRequestConfig(t)
	opts := liveDirectOptions(nil)
	if mutate != nil {
		mutate(cfg, &opts)
	}
	o, rec := newLiveRequestBrain(t, cfg, nil)
	o.WarmupTurn(t.Context(), opts)
	out = append(out, normalizeLiveRequest(cfg, rec.requests())...)
	return strings.Join(out, "\n") + "\n"
}

func normalizeLiveRequest(cfg *config.Config, bodies [][]byte) []string {
	var out []string
	for _, body := range bodies {
		body = bytes.ReplaceAll(body, []byte(cfg.WorkDir), []byte("/work"))
		body = bytes.ReplaceAll(body, []byte(cfg.PromptsDir), []byte("/prompts"))
		out = append(out, string(body))
	}
	return out
}

// The live direct request and the warm-up request are pinned byte for byte:
// a change to what an ordinary turn sends has to show up here.
func TestLiveDirectRequestsMatchTheirGolden(t *testing.T) {
	got := liveDirectRequests(t, nil)
	path := filepath.Join("testdata", "live_direct_requests.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Fatalf("live direct requests changed\n got: %s\nwant: %s", got, want)
	}
}
