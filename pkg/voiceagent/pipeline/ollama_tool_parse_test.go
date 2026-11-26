package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// A persistent parser failure must remain a visible error, without entering
// synthesis or claiming to speak. Exercise the actual Ollama HTTP adapter.
func TestOllamaMalformedToolFailureDoesNotStartSpeech(t *testing.T) {
	const failure = "XML syntax error on line 25: unexpected EOF"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		if r.URL.Path == "/api/show" {
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
			return
		}
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": failure})
	}))
	defer server.Close()
	root := t.TempDir()
	provider, err := brain.NewOllama(&config.Config{
		BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest",
		PromptsDir: filepath.Join(root, "prompts"), WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var speechEvents, retryNotices atomic.Int32
	var failureEvent, response atomic.Value
	events.Subscribe(bus, func(events.GeneratingVoice) { speechEvents.Add(1) })
	events.Subscribe(bus, func(events.SpeakingStarted) { speechEvents.Add(1) })
	events.Subscribe(bus, func(e events.Info) {
		if strings.Contains(e.Message, "malformed tool-call syntax") {
			retryNotices.Add(1)
		}
	})
	events.Subscribe(bus, func(e events.Error) { failureEvent.Store(e) })
	events.Subscribe(bus, func(e events.ResponseReady) { response.Store(e) })
	synthesizer := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{STT: &fakeSTT{text: "Which books are in my library?"}, Brain: provider,
		TTS: synthesizer, Player: player, Events: bus, VoiceToolsEnabled: true}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || retryNotices.Load() != 1 {
		t.Fatalf("requests=%d retry notices=%d", requests.Load(), retryNotices.Load())
	}
	if speechEvents.Load() != 0 || len(synthesizer.Texts()) != 0 {
		t.Fatalf("parser failure produced speech: events=%d text=%v", speechEvents.Load(), synthesizer.Texts())
	}
	e, ok := failureEvent.Load().(events.Error)
	if !ok || e.Stage != "brain" || !strings.Contains(e.Message, failure) {
		t.Fatalf("lost failure detail: %+v", e)
	}
	ready, ok := response.Load().(events.ResponseReady)
	if !ok || !ready.Degraded || ready.Response != "" {
		t.Fatalf("fabricated or missing terminal response: %+v", ready)
	}
	for _, turn := range provider.History() {
		if turn.Role == "assistant" {
			t.Fatalf("parser failure became assistant history: %+v", turn)
		}
	}
}

func TestChatOnlyVoiceTurnRecoversBeforeSpeech(t *testing.T) {
	const answer = "I can explain the command from our conversation."
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		var req struct{ Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Tools) != 0 {
			t.Errorf("expected a tools-free request, got %+v (%v)", req, err)
		}
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true,"done_reason":"stop"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": answer}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	root := t.TempDir()
	provider, err := brain.NewOllama(&config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest",
		PromptsDir: filepath.Join(root, "prompts"), WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	events.Subscribe(bus, func(e events.ToolCallStarted) { t.Errorf("unexpected execution: %s", e.Name) })
	synth := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{STT: &fakeSTT{text: "Can you explain that command?"}, Brain: provider, TTS: synth, Player: player, Events: bus}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(synth.Texts(), " "); got != answer || requests.Load() != 2 {
		t.Fatalf("speech=%q requests=%d", got, requests.Load())
	}
	if turns := provider.History(); len(turns) != 2 || turns[1].Content != answer || len(turns[1].ToolCalls) != 0 {
		t.Fatalf("history=%+v", turns)
	}
}
