package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ollama/ollama/api"
)

// textOnlyChat is a conversation whose selected persona runs on an Ollama
// model that cannot view images, with the real provider and pipeline.
type textOnlyChat struct {
	model    conversationModel
	brain    *brain.OllamaBrain
	pipeline *pipeline.Pipeline
	cfg      *config.Config
	client   *http.Client
	// showDown makes the server unable to describe its model.
	showDown atomic.Bool
	// showHangs makes the server leave that question unanswered. asked gets
	// each one it leaves hanging.
	showHangs atomic.Bool
	asked     chan struct{}
	bus       *events.Bus
	// shade tells the pictures attached by attachNew apart.
	shade uint8
	mu    sync.Mutex
	sent  []api.ChatRequest
}

// noRecordings resolves no audio: these conversations are typed.
type noRecordings struct{}

func (noRecordings) ResolveAudio(context.Context, utterance.AudioRef) ([]byte, error) {
	return nil, errors.New("no recording")
}

func newTextOnlyChat(t *testing.T) *textOnlyChat {
	t.Helper()
	return newOllamaChat(t, false)
}

// newOllamaChat builds the conversation on the ordinary Ollama provider, or on
// the one a native-audio persona runs on.
func newOllamaChat(t *testing.T, nativeAudio bool) *textOnlyChat {
	t.Helper()
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	c := &textOnlyChat{asked: make(chan struct{}, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if c.showHangs.Load() {
				// The server notices the caller leaving only once the body is read.
				_, _ = io.Copy(io.Discard, r.Body)
				c.asked <- struct{}{}
				<-r.Context().Done()
				return
			}
			if c.showDown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "model is loading"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
		case "/api/chat":
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			c.mu.Lock()
			c.sent = append(c.sent, req)
			c.mu.Unlock()
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: "fixture", Message: api.Message{Role: "assistant", Content: "I cannot view it."}, Done: true, DoneReason: "stop"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture"}}})
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture", OllamaNumCtx: 8192, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), AgentName: "Venessa", MaxHistory: 20}
	var err error
	if nativeAudio {
		c.brain, err = brain.NewNativeOllama(t.Context(), cfg, server.Client(), noRecordings{})
	} else {
		c.brain, err = brain.NewOllama(cfg)
	}
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	c.cfg, c.client, c.bus = cfg, server.Client(), bus
	c.pipeline = &pipeline.Pipeline{Brain: c.brain, Events: bus}
	c.model = sizedConversation(t, 100, 40)
	c.model.cfg = cfg
	c.model.deps = conversationDeps{runner: c.pipeline, ctx: t.Context(), bus: bus}
	return c
}

// submit sends the draft through its check. It returns the command that runs
// the turn, or nil when the draft was refused.
func (c *textOnlyChat) submit(t *testing.T, text string) tea.Cmd {
	t.Helper()
	c.model.input.SetValue(text)
	check := c.model.handleSubmit()
	if check == nil {
		t.Fatal("missing draft check")
	}
	send, _ := c.model.updateMedia(check())
	return send
}

// attach puts one picture in the draft, as a paste or a dropped file does.
func (c *textOnlyChat) attach(t *testing.T) chatmedia.Ref {
	t.Helper()
	cmd := c.model.importMedia(nil, testmedia.PNG())
	if cmd == nil {
		t.Fatal("missing import")
	}
	c.model.updateMedia(cmd())
	return c.model.media.draft[len(c.model.media.draft)-1]
}

func (c *textOnlyChat) requests() []api.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.ChatRequest(nil), c.sent...)
}

// The picture is for the conversation, so the draft is sent. The selected
// model answers without it and the record keeps it for a persona that views
// images.
func TestPictureDraftIsSentWhileATextOnlyModelIsSelected(t *testing.T) {
	c := newTextOnlyChat(t)
	ref := c.attach(t)
	c.model.input.SetValue("What is on my screen?")
	check := c.model.handleSubmit()
	if check == nil {
		t.Fatal("missing draft check")
	}
	send, _ := c.model.updateMedia(check())
	if c.model.media.err != "" || send == nil {
		t.Fatalf("the draft was refused: %q", c.model.media.err)
	}
	if done, ok := send().(textTurnDoneMsg); !ok || done.err != nil {
		t.Fatalf("turn: %+v", done)
	}
	sent := c.requests()
	if len(sent) == 0 {
		t.Fatal("the turn was not sent")
	}
	for _, req := range sent {
		told := false
		for _, m := range req.Messages {
			if len(m.Images) > 0 {
				t.Fatal("a model that cannot view images was sent one")
			}
			told = told || strings.Contains(m.Content, "not shown, because this model cannot view images.")
		}
		if !told {
			t.Fatal("the model was not told a picture was left out")
		}
	}
	history := c.brain.History()
	if len(history) != 2 || history[0].Content != "What is on my screen?" || len(history[0].Attachments) != 1 || history[0].Attachments[0] != ref {
		t.Fatalf("the conversation record: %+v", history)
	}
}

// A picture that cannot be read is still refused at the draft, before it can
// enter the conversation and fail every persona that views images.
func TestUnreadablePictureDraftIsRefusedWhileATextOnlyModelIsSelected(t *testing.T) {
	c := newTextOnlyChat(t)
	ref := c.attach(t)
	if err := os.Remove(chatmedia.DefaultStore().Path(ref)); err != nil {
		t.Fatal(err)
	}
	c.model.input.SetValue("What is on my screen?")
	check := c.model.handleSubmit()
	if check == nil {
		t.Fatal("missing draft check")
	}
	send, _ := c.model.updateMedia(check())
	if c.model.media.err == "" || send != nil || len(c.model.media.draft) != 1 || c.model.input.Value() != "What is on my screen?" {
		t.Fatalf("an unreadable picture was not refused with its draft kept: err=%q", c.model.media.err)
	}
	if len(c.requests()) != 0 || len(c.brain.History()) != 0 {
		t.Fatal("a refused picture reached the model or the record")
	}
}

// A persona that takes native audio cannot be sent a picture at all: its turn
// would fail after the draft was gone. The draft is refused while it can
// still be changed.
func TestPictureDraftIsRefusedForANativeAudioPersona(t *testing.T) {
	c := newOllamaChat(t, true)
	c.attach(t)
	c.model.input.SetValue("What is on my screen?")
	check := c.model.handleSubmit()
	if check == nil {
		t.Fatal("missing draft check")
	}
	send, _ := c.model.updateMedia(check())
	if send != nil {
		done, _ := send().(textTurnDoneMsg)
		t.Fatalf("the draft was sent and its turn ended with: %v", done.err)
	}
	if !strings.Contains(c.model.media.err, "native audio") || len(c.model.media.draft) != 1 || c.model.input.Value() != "What is on my screen?" {
		t.Fatalf("the draft was not refused and kept: err=%q", c.model.media.err)
	}
	if len(c.requests()) != 0 || len(c.brain.History()) != 0 {
		t.Fatal("a refused picture reached the model or the record")
	}
}

// A server that cannot describe its model refuses the draft while it can
// still be changed, as it did when admission asked what the model can view.
func TestPictureDraftIsKeptWhenTheServerCannotDescribeTheModel(t *testing.T) {
	c := newTextOnlyChat(t)
	c.attach(t)
	c.showDown.Store(true)
	if send := c.submit(t, "What is on my screen?"); send != nil {
		done, _ := send().(textTurnDoneMsg)
		t.Fatalf("the draft was sent and its turn ended with: %v", done.err)
	}
	if !strings.Contains(c.model.media.err, "check image support") || len(c.model.media.draft) != 1 || c.model.input.Value() != "What is on my screen?" {
		t.Fatalf("the draft was not refused and kept: err=%q", c.model.media.err)
	}
	if len(c.requests()) != 0 || len(c.brain.History()) != 0 {
		t.Fatal("a refused picture reached the model or the record")
	}
}
