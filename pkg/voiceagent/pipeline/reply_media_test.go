package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/ollama/ollama/api"
)

func imageWorkerFixture(t *testing.T) (*config.Config, *brain.OllamaBrain, chatmedia.Ref, string, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	ref, err := chatmedia.DefaultStore().Import("input.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "result.gif")
	if err = os.WriteFile(output, testmedia.GIF(), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision", "tools"}})
		case "/api/chat":
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			found := false
			for _, m := range req.Messages {
				for _, image := range m.Images {
					if bytes.Equal(image, testmedia.PNG()) {
						found = true
					}
				}
			}
			if !found {
				t.Error("worker request did not contain actual image bytes")
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: "fixture", Message: api.Message{Role: "assistant", Content: "I inspected the two blocks.\n![Animated result](<" + output + ">)"}, Done: true})
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{ContinuityIntegrityEnabled: true, ContinuityRecoveryMax: 2, MaxHistory: 20, OllamaHost: server.URL, OllamaModel: "fixture", VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), AgentName: "Obey"}
	worker, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, worker, ref, output, started, release
}
func TestWorkerImagesSurviveAdmissionAndPersistReply(t *testing.T) {
	cfg, worker, ref, output, started, release := imageWorkerFixture(t)
	p := &Pipeline{Brain: worker, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("images"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, worker, cfg, nil)
	a.Wire()
	p.incActiveTurn()
	defer p.decActiveTurn()
	done := make(chan events.JobFinished, 1)
	events.Subscribe(p.Events, func(e events.JobFinished) { done <- e })
	ctx, cancel := context.WithCancel(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}))
	defer cancel()
	metrics := newTurnMetrics()
	a.handleSpawn(ctx, p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Inspect the image and return the animation"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started")
	}
	cancel()
	close(release) // Foreground cancellation must not discard admitted work.
	var result events.JobFinished
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never finished")
	}
	if len(result.Attachments) != 1 || strings.Contains(result.Summary, "![") {
		t.Fatalf("result %+v", result)
	}
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	saved := session.New("ollama", "fixture")
	p.OnTurn = func() {
		if err := store.Save(saved, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	turns := p.Brain.History()
	last := turns[len(turns)-1]
	if len(last.Attachments) != 1 || last.Attachments[0] != result.Attachments[0] {
		t.Fatalf("history %+v", turns)
	}
	items := p.Workers()
	if len(items) != 1 || len(items[0].Thread[0].Attachments) != 1 || len(items[0].Thread[len(items[0].Thread)-1].Attachments) != 1 {
		t.Fatalf("thread %+v", items)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if _, err := chatmedia.DefaultStore().Preview(last.Attachments[0]); err != nil {
		t.Fatal("returned asset did not survive source removal:", err)
	}
	restored, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Turns[len(restored.Turns)-1].Attachments) != 1 {
		t.Fatal("saved reply lost images")
	}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if len(p.Workers()) != 0 {
		t.Fatal("image worker crossed conversation boundary")
	}
}
func TestForegroundImageReplyBecomesGroupCompletion(t *testing.T) {
	_, provider, ref, _, _, release := imageWorkerFixture(t)
	close(release)
	p := &Pipeline{Brain: provider, Events: events.NewBus()}
	var response events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { response = e })
	state := &completionState{}
	ctx := withCompletionState(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}), state)
	if err := p.RunTurnTextMode(ctx, "Inspect and return the animation"); err != nil {
		t.Fatal(err)
	}
	if len(response.Attachments) != 1 || strings.Contains(response.Response, "![") {
		t.Fatalf("response %+v", response)
	}
	// The group completion consumes the same durable reply, not a second import.
	metrics := newTurnMetrics()
	// The actual turn already attached media; test the turn completion path with its finalization state.
	metrics.mediaReply = &response
	p.completeAgentResponse(ctx, response.Response, metrics, false)
	if !state.ok || len(state.turn.Attachments) != 1 || state.turn.Content != response.Response {
		t.Fatalf("group completion %+v", state)
	}
}
func TestUnsupportedWorkerImagesAndSpeechSuppression(t *testing.T) {
	if err := validateProviderImages(t.Context(), &slowWorker{}, []chatmedia.Ref{{ID: "image"}}); err == nil {
		t.Fatal("unsupported worker accepted images")
	}
	projection := NewSpeechProjection()
	for _, chunk := range []string{"![Animation](</tmp/result.", "gif>)"} {
		if got := projection.Speak(chunk).Speakable; got != "" {
			t.Fatalf("image path reached speech: %q", got)
		}
	}
	if got := projection.Speak("Here is your animation.").Speakable; got == "" {
		t.Fatal("image suppressed later prose")
	}
}
