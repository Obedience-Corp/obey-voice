package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	router, err := newSemanticSkillRouter(context.Background(), embedder, "fixture", .55, []skills.Skill{{Name: "web-perf", Description: "Audit website loading performance", Body: "SPECIALIZED_BODY"}})
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
