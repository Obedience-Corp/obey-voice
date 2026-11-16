package brain

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// visionFake routes every turn to chat and answers it, recording requests.
type visionFake struct {
	mu       sync.Mutex
	requests []api.ChatRequest
}

func (f *visionFake) brain(t *testing.T, cfg *config.Config, system string) *OllamaBrain {
	t.Helper()
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
		f.mu.Lock()
		f.requests = append(f.requests, req)
		seq := len(f.requests)
		f.mu.Unlock()
		msg := api.Message{Role: "assistant", Content: "It is a grey rectangle."}
		if hasTool(req.Tools, "route") {
			args := api.NewToolCallFunctionArguments()
			args.Set("kind", "chat")
			msg = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: fmt.Sprintf("call-%d", seq), Function: api.ToolCallFunction{Name: "route", Arguments: args}}}}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: msg, Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 4}})
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: system, cfg: cfg}
}

func (f *visionFake) sent() []api.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ChatRequest(nil), f.requests...)
}

// imageTurnsIn lists the turn labels of the user messages that carry images.
func imageTurnsIn(messages []api.Message) []string {
	var turns []string
	for _, m := range messages {
		if m.Role == "user" && len(m.Images) > 0 {
			_, content := decodeOllamaUser(m.Content)
			turns = append(turns, strings.Fields(content)[0])
		}
	}
	return turns
}

// oldWindowImageTurns replays the earlier rule, 12 stored messages cut back to
// 10 after each reply, for four-message routed turns, and returns how many
// image-bearing turns each turn's requests carried under it.
func oldWindowImageTurns(turns int, hasImage func(int) bool) []int {
	var history []api.Message
	var owner []int
	counts := make([]int, turns+1)
	for n := 1; n <= turns; n++ {
		carried := map[int]bool{n: true}
		for _, k := range owner {
			carried[k] = true
		}
		for k := range carried {
			if hasImage(k) {
				counts[n]++
			}
		}
		history = append(history,
			api.Message{Role: "user"},
			api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{}}},
			api.Message{Role: "tool"},
			api.Message{Role: "assistant"})
		owner = append(owner, n, n, n, n)
		if len(history) > 12 {
			start := historyWindowStart(history, 10)
			history, owner = history[start:], owner[start:]
		}
	}
	return counts
}

func runImageConversation(t *testing.T, turns int, hasImage func(int) bool) (*OllamaBrain, *visionFake, [][2]int) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	perTurn := make([][2]int, turns+1)
	for n := 1; n <= turns; n++ {
		opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
		if hasImage(n) {
			opts.Attachments = photos(t, 1, 32, 32)
			opts.Attachments[0].Name = fmt.Sprintf("turn-%02d.png", n)
		}
		before := len(fake.sent())
		if turn := runRoutedTurn(t, o, fmt.Sprintf("turn-%02d look at this", n), opts); turn.result.Err != nil {
			t.Fatalf("turn %d: %v", n, turn.result.Err)
		}
		sent := fake.sent()[before:]
		if len(sent) != 2 {
			t.Fatalf("turn %d sent %d requests", n, len(sent))
		}
		for i, req := range sent {
			images := imageTurnsIn(req.Messages)
			if hasImage(n) && !slices.Contains(images, fmt.Sprintf("turn-%02d", n)) {
				t.Fatalf("turn %d: request %d lost the image of the turn being answered", n, i)
			}
			perTurn[n][i] = len(images)
		}
		assertWorkingAligned(t, o)
	}
	return o, fake, perTurn
}

func TestImagesStayNoLongerThanTheOldWindowKeptThem(t *testing.T) {
	const turns = 16
	for name, hasImage := range map[string]func(int) bool{
		"every turn":        func(int) bool { return true },
		"first three turns": func(n int) bool { return n <= 3 },
		"every other turn":  func(n int) bool { return n%2 == 1 },
	} {
		t.Run(name, func(t *testing.T) {
			_, _, perTurn := runImageConversation(t, turns, hasImage)
			old := oldWindowImageTurns(turns, hasImage)
			for n := 1; n <= turns; n++ {
				for call, got := range perTurn[n] {
					if got > old[n] || got > imageTurns {
						t.Fatalf("turn %d call %d carried %d image turns; the old window carried %d", n, call, got, old[n])
					}
				}
			}
		})
	}
}

func TestAgedImageTurnKeepsItsTextEverywhereElse(t *testing.T) {
	o, _, _ := runImageConversation(t, 5, func(n int) bool { return n == 1 })
	first := o.history[0]
	if len(first.Images) != 0 || !strings.HasSuffix(first.Content, " (turn-01.png): no longer attached.") || !strings.Contains(first.Content, "turn-01 look at this") {
		t.Fatalf("aged image turn reads %q with %d images", first.Content, len(first.Images))
	}
	if len(o.working[0].Attachments) != 1 || len(o.History()[0].Attachments) != 1 || o.History()[0].Content != "turn-01 look at this" {
		t.Fatal("the transcript or working view lost the image")
	}

	// A restored session and a persona switch apply the same rule.
	for name, load := range map[string]func(*OllamaBrain){
		"restore": func(r *OllamaBrain) { r.LoadHistory(o.History()) },
		"switch":  func(r *OllamaBrain) { _ = r.ImportContinuity(o.ContinuitySnapshot()) },
	} {
		r := &OllamaBrain{cfg: budgetConfig()}
		load(r)
		if got := imageTurnsIn(r.history); len(got) != 0 || !strings.HasSuffix(r.history[0].Content, "no longer attached.") {
			t.Fatalf("%s brought back image turns %v", name, got)
		}
	}

	// Once evicted, the archive note reads as it always did.
	o.history = append([]api.Message(nil), o.history[:4]...)
	o.working = append([]Turn(nil), o.working[:4]...)
	o.archiveTurns(o.history, o.working)
	if note := o.ContinuityArchive()[0].Summary; note != "user: turn-01 look at this\nassistant: It is a grey rectangle.\n" {
		t.Fatalf("archive note = %q", note)
	}
}
