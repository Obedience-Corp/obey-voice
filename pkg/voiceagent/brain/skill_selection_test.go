package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

func skillSelectionClient(t *testing.T, answer string) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Tools) > 0 || len(req.Messages) != 2 || strings.Contains(req.Messages[1].Content, "SPECIALIZED_BODY") {
			t.Errorf("selection received tools/history/skill body: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: answer}, Done: true})
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	return api.NewClient(base, http.DefaultClient)
}
func TestSkillActivationRequiresConfirmedCandidate(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           int
		fail           bool
	}{
		{"reject word overlap", `{"relevant":false,"names":[]}`, 0, false},
		{"reject copied names without relevance", `{"relevant":false,"names":["web-perf"]}`, 0, false},
		{"accept applicable task", `{"relevant":true,"names":["web-perf"]}`, 1, false},
		{"unknown", `{"relevant":true,"names":["made-up"]}`, 0, true},
		{"duplicate", `{"relevant":true,"names":["web-perf","web-perf"]}`, 0, true},
		{"missing", `{}`, 0, true},
		{"null", `{"relevant":false,"names":null}`, 0, true},
		{"trailing object", `{"relevant":true,"names":[]} {}`, 0, true},
		{"prose", `I choose web-perf`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &OllamaBrain{client: skillSelectionClient(t, tc.response), cfg: &config.Config{PromptsDir: t.TempDir()}, model: "fixture"}
			candidates := []skills.Skill{{Name: "web-perf", Description: "Audit website loading performance", Body: "SPECIALIZED_BODY"}}
			got, err := o.confirmSkillMatches(t.Context(), "Can you look up the weather?", candidates)
			if (err != nil) != tc.fail || len(got) != tc.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
			if len(o.history) > 0 || len(o.History()) > 0 {
				t.Fatal("private selection changed conversation history")
			}
		})
	}
}
func TestRejectedEmbeddingCandidatesDoNotActivateSkillBodies(t *testing.T) {
	embedder := &routingEmbedder{catalog: [][]float32{{1, 0}}, queries: map[string][]float32{"weather": {1, 0}}}
	router, err := newSemanticSkillRouter(context.Background(), embedder, "fixture", .55, []skills.Skill{{Name: "web-perf", Description: "Audit website loading performance", Body: "SPECIALIZED_BODY"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{client: skillSelectionClient(t, `{"relevant":false,"names":[]}`), skillRouter: router, cfg: &config.Config{PromptsDir: t.TempDir()}, model: "fixture"}
	starts := 0
	got := o.routeSkillContext(t.Context(), "weather", func(ToolCall) { starts++ }, nil)
	if got != "" || starts != 0 {
		t.Fatalf("irrelevant skill activated: %q events=%d", got, starts)
	}
}

func TestOptionalSkillSelectionPreservesTheTaskBudget(t *testing.T) {
	for _, limits := range []BudgetLimits{{MaxRequests: 1}, {MaxRequests: 4, MaxOutputTokens: 64}} {
		budget := NewWorkerBudget(limits)
		o := &OllamaBrain{}
		selected, err := o.confirmSkillMatches(budget.Context(t.Context()), "Use a skill", []skills.Skill{{Name: "skill"}})
		if err != nil || len(selected) != 0 || budget.Snapshot().Requests != 0 {
			t.Fatalf("selection spent the task's last allowance: %v %v %+v", selected, err, budget.Snapshot())
		}
		if err := budget.AcquireRequest(t.Context()); err != nil {
			t.Fatalf("task request cannot run: %v", err)
		}
	}
}

func recordingSelectionServer(t *testing.T, chats *[]api.ChatRequest) *api.Client {
	t.Helper()
	return recordingSelectionServerWith(t, chats, `{"relevant":false,"names":[]}`)
}

func recordingSelectionServerWith(t *testing.T, chats *[]api.ChatRequest, selection string) *api.Client {
	t.Helper()
	encoded, _ := json.Marshal(selection)
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		*chats = append(*chats, req)
		mu.Unlock()
		switch {
		case len(req.Format) > 0:
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":`+string(encoded)+`},"done":true}`+"\n")
		case len(req.Tools) > 0:
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
		default:
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Hello."},"done":true}`+"\n")
		}
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	return api.NewClient(base, http.DefaultClient)
}

func runnerOptions(t *testing.T, req api.ChatRequest) api.Runner {
	t.Helper()
	raw, err := json.Marshal(req.Options)
	if err != nil {
		t.Fatal(err)
	}
	var runner api.Runner
	if err := json.Unmarshal(raw, &runner); err != nil {
		t.Fatal(err)
	}
	return runner
}

// Ollama reloads the model when runner options differ between requests, so
// the private selection must not size the runner differently from the turn.
func TestLiveSkillSelectionSharesTheTurnRunnerOptions(t *testing.T) {
	var chats []api.ChatRequest
	embedder := &routingEmbedder{catalog: [][]float32{{1, 0}}, queries: map[string][]float32{"hello": {1, 0}}}
	router, err := newSemanticSkillRouter(t.Context(), embedder, "embed", .55, []skills.Skill{{Name: "greeter", Description: "Greet people", Body: "SPECIALIZED_BODY"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), skillRouter: router, model: "m", workDir: t.TempDir(),
		cfg: &config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128, MaxHistory: 10, PromptsDir: t.TempDir()}}
	stream, err := o.ThinkStream(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := drainStream(t, stream); text != "Hello." {
		t.Fatalf("answer = %q", text)
	}
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(chats) != 3 {
		t.Fatalf("chat requests = %d, want selection, route and answer", len(chats))
	}
	selection, route, answer := chats[0], chats[1], chats[2]
	if len(selection.Format) == 0 || len(selection.Tools) != 0 || len(route.Tools) == 0 || len(answer.Tools) != 0 {
		t.Fatalf("unexpected request order: selection format=%s tools=%d, route tools=%d, answer tools=%d", selection.Format, len(selection.Tools), len(route.Tools), len(answer.Tools))
	}
	if r := runnerOptions(t, selection); r.NumCtx != 8192 || r.NumBatch != 128 {
		t.Fatalf("selection runner options = %+v", r)
	}
	for name, req := range map[string]api.ChatRequest{"route": route, "answer": answer} {
		if !reflect.DeepEqual(runnerOptions(t, selection), runnerOptions(t, req)) || !reflect.DeepEqual(selection.Shift, req.Shift) {
			t.Errorf("selection runner options differ from %s: %+v vs %+v", name, runnerOptions(t, selection), runnerOptions(t, req))
		}
	}
	if selection.Stream == nil || *selection.Stream || thinkEnabled(selection.Think) || len(selection.Messages) != 2 {
		t.Fatalf("selection lost its private shape: %+v", selection)
	}
	for key, want := range map[string]float64{"temperature": 0, "seed": 42, "num_predict": 128} {
		if got, ok := selection.Options[key].(float64); !ok || got != want {
			t.Errorf("selection %s = %v, want %v", key, selection.Options[key], want)
		}
	}
}

func TestWorkerSkillSelectionKeepsItsPrivateWindow(t *testing.T) {
	for name, worker := range map[string]struct {
		cfg *config.Config
		ctx func(context.Context) context.Context
	}{
		"worker config": {&config.Config{IsWorker: true, OllamaNumCtx: 8192, OllamaNumBatch: 128}, func(ctx context.Context) context.Context { return ctx }},
		"worker budget": {&config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128}, NewWorkerBudget(BudgetLimits{MaxRequests: 10}).Context},
	} {
		t.Run(name, func(t *testing.T) {
			var chats []api.ChatRequest
			worker.cfg.PromptsDir = t.TempDir()
			o := &OllamaBrain{client: recordingSelectionServer(t, &chats), model: "m", cfg: worker.cfg}
			if _, err := o.confirmSkillMatches(worker.ctx(t.Context()), "hello", []skills.Skill{{Name: "greeter", Description: "Greet people"}}); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"num_ctx": float64(4096), "num_predict": float64(128), "seed": float64(42), "temperature": float64(0)}
			if len(chats) != 1 || !reflect.DeepEqual(chats[0].Options, want) {
				t.Fatalf("worker selection options = %+v", chats)
			}
		})
	}
}

func TestLiveSkillSelectionKeepsItsInputBound(t *testing.T) {
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), model: "m", cfg: &config.Config{OllamaNumCtx: 32768, PromptsDir: t.TempDir()}}
	candidates := []skills.Skill{{Name: "long", Description: strings.Repeat("d", 8000)}}
	_, err := o.confirmSkillMatches(t.Context(), strings.Repeat("x", 8000), candidates)
	if err == nil || !strings.Contains(err.Error(), "safe context budget") || len(chats) != 0 {
		t.Fatalf("oversized selection err=%v requests=%d", err, len(chats))
	}
}

func runSelectionTurn(t *testing.T, o *OllamaBrain) {
	t.Helper()
	stream, err := o.ThinkStream(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
}

func selectionTestRouter(t *testing.T, body string) *semanticSkillRouter {
	t.Helper()
	embedder := &routingEmbedder{catalog: [][]float32{{1, 0}}, queries: map[string][]float32{"hello": {1, 0}}}
	router, err := newSemanticSkillRouter(t.Context(), embedder, "embed", .55, []skills.Skill{{Name: "greeter", Description: "Greet people", Body: body}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestLiveSkillSelectionSharesAnExpandedWindowWithTheTurn(t *testing.T) {
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), skillRouter: selectionTestRouter(t, "body"), model: "m", workDir: t.TempDir(),
		cfg: &config.Config{OllamaNumCtx: 2048, ContinuityNumCtxCeil: 32768, MaxHistory: 10, PromptsDir: t.TempDir()}}
	o.contextHighWater = 8192
	runSelectionTurn(t, o)
	if len(chats) != 3 {
		t.Fatalf("chat requests = %d, want selection, route and answer", len(chats))
	}
	for i, req := range chats {
		if got := runnerOptions(t, req).NumCtx; got != 8192 {
			t.Errorf("request %d num_ctx = %d, want the carried high-water mark 8192", i, got)
		}
	}
}

// The window grows when an activated skill pushes the prompt past the current
// one, which is only known after the selection returns. The route round then
// reloads once, the same single reload the growth costs whichever call
// carries it; selection cannot size itself for a body it has not chosen yet.
func TestLiveSkillSelectionWindowGrowsOnceWhenTheSkillBodyDoesNotFit(t *testing.T) {
	var chats []api.ChatRequest
	client := recordingSelectionServerWith(t, &chats, `{"relevant":true,"names":["greeter"]}`)
	o := &OllamaBrain{client: client, skillRouter: selectionTestRouter(t, strings.Repeat("x", 30000)), model: "m", workDir: t.TempDir(),
		cfg: &config.Config{OllamaNumCtx: 2048, ContinuityNumCtxCeil: 32768, MaxHistory: 10, PromptsDir: t.TempDir()}}
	o.contextHighWater = 8192
	runSelectionTurn(t, o)
	if len(chats) != 3 {
		t.Fatalf("chat requests = %d, want selection, route and answer", len(chats))
	}
	selection, route, answer := runnerOptions(t, chats[0]), runnerOptions(t, chats[1]), runnerOptions(t, chats[2])
	if selection.NumCtx != 8192 || route.NumCtx <= selection.NumCtx || route.NumCtx != answer.NumCtx || route.NumCtx != o.numCtxFor(o.contextHighWater) {
		t.Fatalf("num_ctx selection=%d route=%d answer=%d high-water=%d", selection.NumCtx, route.NumCtx, answer.NumCtx, o.contextHighWater)
	}
}

func TestLiveSkillSelectionWithAnAudioResolverSharesTruncationOptions(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), audioResolver: media, model: "m", workDir: t.TempDir(),
		cfg: &config.Config{OllamaNumBatch: 128, PromptsDir: t.TempDir()}}
	if _, err := o.confirmSkillMatches(t.Context(), "hello", []skills.Skill{{Name: "greeter", Description: "Greet people"}}); err != nil {
		t.Fatal(err)
	}
	want := o.newChatRequest(nil, nil)
	got := chats[0]
	if got.Truncate == nil || got.Shift == nil || *got.Truncate || *got.Shift {
		t.Fatalf("selection truncate=%v shift=%v, want both false", got.Truncate, got.Shift)
	}
	if !reflect.DeepEqual(got.Truncate, want.Truncate) || !reflect.DeepEqual(got.Shift, want.Shift) {
		t.Fatalf("selection truncate/shift differ from the turn request")
	}
	if r := runnerOptions(t, got); r.NumCtx != nativeContextCeiling || r.NumBatch != 128 {
		t.Fatalf("selection runner options = %+v, want num_ctx %d", r, nativeContextCeiling)
	}
}

func TestLiveSkillSelectionSendsNoWindowWhenNoneIsConfigured(t *testing.T) {
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), model: "m", cfg: &config.Config{PromptsDir: t.TempDir()}}
	if _, err := o.confirmSkillMatches(t.Context(), "hello", []skills.Skill{{Name: "greeter", Description: "Greet people"}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"num_predict": float64(128), "seed": float64(42), "temperature": float64(0)}
	if !reflect.DeepEqual(chats[0].Options, want) {
		t.Fatalf("selection options = %v, want only the private sampling options", chats[0].Options)
	}
}
