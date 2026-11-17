package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// photoTurnBrain is an Ollama brain with a realistic 6 KB system prompt, the
// default conversation settings and a server that routes to chat and answers.
func photoTurnBrain(t *testing.T, cfg *config.Config) (*OllamaBrain, func() []api.ChatRequest) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	var mu sync.Mutex
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision", "tools"}})
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		msg := api.Message{Role: "assistant", Content: "Those are two grey photos of the same size."}
		if len(req.Tools) == 1 && req.Tools[0].Function.Name == "route" {
			args := api.NewToolCallFunctionArguments()
			args.Set("kind", "chat")
			msg = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "route", Function: api.ToolCallFunction{Name: "route", Arguments: args}}}}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: msg, Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 8}})
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: strings.Repeat("You are Obey, a local voice assistant. ", 160), cfg: cfg}
	return o, func() []api.ChatRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]api.ChatRequest(nil), requests...)
	}
}

func photos(t *testing.T, n, w, h int) []chatmedia.Ref {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	var refs []chatmedia.Ref
	for i := range n {
		ref, err := chatmedia.DefaultStore().Import(fmt.Sprintf("photo-%d.png", i), append(bytes.Clone(buf.Bytes()), byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	return refs
}

func requestShape(req api.ChatRequest) (users, images int) {
	for _, m := range req.Messages {
		if m.Role == "user" {
			users++
		}
		images += len(m.Images)
	}
	return users, images
}

func defaultConversationConfig() *config.Config {
	return &config.Config{MaxHistory: 40, OllamaNumCtx: 32768, ContinuityNumCtxCeil: 32768, ContinuityProjectionEnabled: true,
		ContinuityArchiveEnabled: true, ContinuitySoftLimitRatio: 0.8, ContinuityIntegrityEnabled: true, ContinuityRecoveryMax: 2}
}

// Two phone photos after eight text turns keep the conversation, and the
// follow-up still sees both photos.
func TestPhotoTurnKeepsTheConversationAndItsFollowUp(t *testing.T) {
	o, sent := photoTurnBrain(t, defaultConversationConfig())
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
	for n := 1; n <= 8; n++ {
		if turn := runRoutedTurn(t, o, fmt.Sprintf("Text turn %d.", n), opts); turn.result.Err != nil {
			t.Fatal(turn.result.Err)
		}
	}
	for _, step := range []struct {
		input       string
		attachments []chatmedia.Ref
		users       int
	}{
		{"What do these photos show?", photos(t, 2, 3024, 4032), 9},
		{"Which one is brighter?", nil, 10},
	} {
		if o.NeedsContinuityCompact(step.input) {
			t.Fatalf("%q: soft limit engaged", step.input)
		}
		before := len(sent())
		turn := runRoutedTurn(t, o, step.input, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Attachments: step.attachments})
		if turn.result.Err != nil {
			t.Fatalf("%q: %v", step.input, turn.result.Err)
		}
		for _, req := range sent()[before:] {
			if users, images := requestShape(req); users != step.users || images != 2 {
				t.Fatalf("%q sent %d user messages and %d images, want %d and 2", step.input, users, images, step.users)
			}
		}
	}
	if len(o.ContinuityArchive()) != 0 {
		t.Fatal("photo turns evicted earlier history")
	}
}

// A screenshot in an 8k window neither evicts earlier turns nor grows the
// context window.
func TestScreenshotKeepsTheConversationAndTheWindow(t *testing.T) {
	cfg := defaultConversationConfig()
	cfg.OllamaNumCtx = 8192
	o, sent := photoTurnBrain(t, cfg)
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
	for n := 1; n <= 3; n++ {
		if turn := runRoutedTurn(t, o, fmt.Sprintf("Text turn %d.", n), opts); turn.result.Err != nil {
			t.Fatal(turn.result.Err)
		}
	}
	before := len(sent())
	opts.Attachments = photos(t, 1, 3456, 2234)
	if turn := runRoutedTurn(t, o, "What is on this screen?", opts); turn.result.Err != nil {
		t.Fatal(turn.result.Err)
	}
	for _, req := range sent()[before:] {
		if users, images := requestShape(req); users != 4 || images != 1 || req.Options["num_ctx"] != float64(8192) {
			t.Fatalf("sent %d user messages, %d images, num_ctx %v", users, images, req.Options["num_ctx"])
		}
	}
	if o.effectiveNumCtx() != 8192 {
		t.Fatalf("context window grew to %d", o.effectiveNumCtx())
	}
}

// A worker given two phone photos with its task is not refused.
func TestWorkerTaskWithPhotosIsNotRefused(t *testing.T) {
	cfg := defaultConversationConfig()
	cfg.IsWorker = true
	o, sent := photoTurnBrain(t, cfg)
	if _, err := o.ThinkFull(t.Context(), "Describe these photos.", StreamOptions{Attachments: photos(t, 2, 3024, 4032)}); err != nil {
		t.Fatalf("worker refused: %v", err)
	}
	if got := sent(); len(got) != 1 {
		t.Fatalf("worker sent %d requests", len(got))
	}
}
