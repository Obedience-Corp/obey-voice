package brain

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func TestLiveBrainRemembersActivatedSkillsAcrossAConfirmingTurn(t *testing.T) {
	embedder := &routingEmbedder{
		catalog: [][]float32{{1, 0}, {0, 1}, {0, 1}},
		queries: map[string][]float32{"navigate": {1, 0}, "yes": {-1, 0}},
	}
	router, err := newSemanticSkillRouter(context.Background(), embedder, "embed", 0.55, routingCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{skillRouter: router, client: skillSelectionClient(t, `{"relevant":true,"names":["campaign-navigation"]}`), model: "fixture"}
	o.routeSkillContext(context.Background(), "navigate", nil, nil)
	want := []string{"campaign-navigation"}
	if got := o.RecentActivatedSkills(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the request: %v, want %v", got, want)
	}
	o.routeSkillContext(context.Background(), "yes", nil, nil)
	if got := o.RecentActivatedSkills(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the confirming turn: %v, want %v", got, want)
	}
	for range recentSkillTurns {
		o.routeSkillContext(context.Background(), "yes", nil, nil)
	}
	if got := o.RecentActivatedSkills(); len(got) != 0 {
		t.Fatalf("after the conversation moved on: %v, want none", got)
	}
}

func TestWorkerInjectsHandedSkillsWithoutAskingItsModel(t *testing.T) {
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServer(t, &chats), model: "tiny", cfg: &config.Config{IsWorker: true},
		skills: []skills.Skill{{Name: "camp-workitems", Description: "List work items", Body: "Run camp wi list."}}}
	ctx := WithActivatedSkills(context.Background(), []string{"camp-workitems", "not-in-this-directory"})
	var started ToolCall
	got := o.routeSkillContext(ctx, "why did the workers die", func(c ToolCall) { started = c }, nil)
	if !strings.Contains(got, "Run camp wi list.") {
		t.Fatalf("the handed skill's body is missing:\n%s", got)
	}
	if strings.Contains(got, "not-in-this-directory") {
		t.Fatalf("a skill this directory does not provide was injected:\n%s", got)
	}
	if len(chats) != 0 {
		t.Fatalf("the worker asked its model %d time(s) to confirm a handed skill", len(chats))
	}
	if started.Summary != "camp-workitems" {
		t.Fatalf("activation hook = %+v", started)
	}
	if got := o.RecentActivatedSkills(); len(got) != 0 {
		t.Fatalf("a worker recorded %v as live activations", got)
	}
}

func TestWorkerWithoutHandedSkillsInjectsNothing(t *testing.T) {
	o := &OllamaBrain{model: "tiny", cfg: &config.Config{IsWorker: true},
		skills: []skills.Skill{{Name: "camp-workitems", Body: "Run camp wi list."}}}
	if got := o.routeSkillContext(context.Background(), "hello", nil, nil); got != "" {
		t.Fatalf("context = %q, want empty", got)
	}
}
