package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type fakeCodex struct {
	requests []codexRequest
	err      error
	response string
}

func (f *fakeCodex) run(ctx context.Context, r codexRequest, emit func(string) error, _ StreamOptions) (codexResult, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return codexResult{}, f.err
	}
	if err := ctx.Err(); err != nil {
		return codexResult{}, err
	}
	if err := emit(f.response); err != nil {
		return codexResult{}, err
	}
	return codexResult{ThreadID: "thread-fixture"}, nil
}
func codexFixture(t *testing.T) (*CodexBrain, *fakeCodex) {
	t.Helper()
	f := &fakeCodex{response: "Hello there."}
	c := &CodexBrain{client: f, cfg: &config.Config{AgentName: "Obey", ActivePersona: "helper", CodexModel: "fixture-model", CodexReasoningEffort: "low", MaxHistory: 2}, systemPrompt: "Persona fixture", turnInstruction: "Speak briefly."}
	return c, f
}
func TestCodexHistoryResumeFailureAndFresh(t *testing.T) {
	c, f := codexFixture(t)
	identity := &TurnIdentity{PersonaID: "helper", PersonaDisplayName: "Helper", PromptHash: "p", ResponseID: "r"}
	if _, err := c.ThinkFull(context.Background(), "remember cobalt", StreamOptions{Identity: identity, OriginalInput: "Hey helper, remember cobalt"}); err != nil {
		t.Fatal(err)
	}
	if c.History()[0].Content != "Hey helper, remember cobalt" || c.History()[0].RoutedInput != "remember cobalt" || c.History()[1].ResponseID != "r" {
		t.Fatalf("identity lost: %+v", c.History())
	}
	_, _ = c.ThinkFull(context.Background(), "recall it", StreamOptions{})
	if r := f.requests[1]; r.ThreadID != "thread-fixture" || strings.Contains(r.Prompt, "remember cobalt") || r.Model != "fixture-model" || r.Effort != "low" || r.System != "Persona fixture" {
		t.Fatalf("bad resume: %+v", r)
	}
	before := len(c.History())
	f.err = errors.New("quota")
	if _, err := c.ThinkFull(context.Background(), "failed", StreamOptions{}); err == nil {
		t.Fatal("lost failure")
	}
	if len(c.History()) != before || c.sessionID != "" || len(f.requests) != 3 {
		t.Fatal("failure mutated history or retried")
	}
	f.err = nil
	_, _ = c.ThinkFull(context.Background(), "next", StreamOptions{})
	if f.requests[3].ThreadID != "" || !strings.Contains(f.requests[3].Prompt, "remember cobalt") {
		t.Fatal("canonical replay missing")
	}
	fresh := c.Fresh().(*CodexBrain)
	if len(fresh.History()) != 0 || fresh.sessionID != "" {
		t.Fatal("worker inherited live conversation")
	}
	fresh.cfg.CodexModel = "worker-model"
	if c.cfg.CodexModel == fresh.cfg.CodexModel {
		t.Fatal("worker shares mutable config")
	}
	if len(c.canonical) != 6 || len(c.working) != 4 {
		t.Fatal("canonical transcript trimmed")
	}
}
func TestCodexTransientContinuationAndTools(t *testing.T) {
	c, f := codexFixture(t)
	c.LoadHistory([]Turn{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "old answer"}})
	c.sessionID = "original"
	_, err := c.ThinkFull(context.Background(), "summarize", StreamOptions{TransientHistory: true, OmitTurnInstruction: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.History()) != 2 || c.sessionID != "original" || f.requests[0].ThreadID != "" || !f.requests[0].Ephemeral {
		t.Fatal("transient request changed live state")
	}
	if strings.Contains(f.requests[0].Prompt, "Speak briefly") {
		t.Fatal("summary got voice instructions")
	}
	_, err = c.ThinkFull(context.Background(), "continue", StreamOptions{AgentContinuation: true, ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.History()) != 3 || c.History()[2].Role != "assistant" || f.requests[1].Tools {
		t.Fatal("continuation invented user turn or tools")
	}
	_, _ = c.ThinkFull(context.Background(), "work", StreamOptions{ToolsEnabled: true})
	if !f.requests[2].Tools || f.requests[2].ThreadID != "" {
		t.Fatal("tool mode reused incompatible native session")
	}
	if err = c.AppendAssistantResponse("host output"); err != nil {
		t.Fatal(err)
	}
	if c.sessionID == "" {
		t.Fatal("host output dropped the native session")
	}
	for range 2 {
		if _, err = c.ThinkFull(context.Background(), "and then", StreamOptions{ToolsEnabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if r := f.requests[3]; r.ThreadID == "" || strings.Count(r.Prompt, `"content":"host output"`) != 1 || strings.Contains(r.Prompt, `"content":"work"`) {
		t.Fatalf("the resumed thread was not sent only the host output:\n%s", r.Prompt)
	}
	if r := f.requests[4]; r.ThreadID == "" || strings.Contains(r.Prompt, "host output") {
		t.Fatalf("the host output was sent twice:\n%s", r.Prompt)
	}
	c.ClearHistory()
	if len(c.History()) != 0 {
		t.Fatal("clear failed")
	}
}
func TestCodexContinuityPreservesCompactionAndCopies(t *testing.T) {
	c, _ := codexFixture(t)
	snapshot := ContinuitySnapshot{Canonical: []Turn{{Role: "user", Content: "full"}}, Working: []Turn{{Role: "assistant", Content: "summary"}}}
	if err := c.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Working[0].Content = "mutated"
	if c.ContextHistory()[0].Content != "summary" || c.History()[0].Content != "full" {
		t.Fatal("continuity views lost")
	}
	h := c.History()
	h[0].Content = "mutated"
	if c.History()[0].Content != "full" {
		t.Fatal("history aliased")
	}
}
func TestCodexStreamFailureAndEmptyReply(t *testing.T) {
	c, f := codexFixture(t)
	f.err = errors.New("transport failed")
	stream, err := c.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
		t.Fatal("error became speech")
	}
	if r := <-stream.Done; r.Err == nil {
		t.Fatal("lost stream failure")
	}
	if len(c.History()) != 0 {
		t.Fatal("failed stream entered transcript")
	}
	f.err = nil
	f.response = ""
	if _, err = c.ThinkFull(context.Background(), "hello", StreamOptions{}); err == nil {
		t.Fatal("empty completion accepted")
	}
}

func TestCodexWorkerFactoriesAndBilling(t *testing.T) {
	client, _ := codexProcessFixture(t, "")
	t.Setenv("PATH", filepath.Dir(client.binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{BrainProvider: "codex", VoiceWorkerProvider: "codex", VoiceWorkerModel: "worker-model"}
	worker, err := NewWorkerProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if worker.(*CodexBrain).cfg.CodexModel != "worker-model" {
		t.Fatal("legacy worker model lost")
	}
	account := "default"
	assignment := config.WorkerAssignment{Provider: "codex", Model: "pinned-model", Billing: config.WorkerBillingSubscription, AccountRef: &account}
	worker, err = NewPlannedWorker(context.Background(), cfg, &config.WorkerPlan{Assignment: assignment})
	if err != nil {
		t.Fatal(err)
	}
	if worker.(*CodexBrain).cfg.CodexModel != "pinned-model" {
		t.Fatal("tier worker model lost")
	}
	assignment.Billing = config.WorkerBillingLocal
	if err = (LocalWorkerCapabilities{}).ValidateWorkerAssignment(assignment); err == nil {
		t.Fatal("Codex accepted local billing")
	}
	caps := CLIWorkerCapabilities("codex")
	if !caps.RuntimeLimit.Supported || caps.AuthoritativeBilling.Supported || caps.ChargeBound.Supported {
		t.Fatal("misstated worker guarantees")
	}
}

func TestCodexBatchIsIsolatedAndKeepsText(t *testing.T) {
	f := &fakeCodex{response: "# Chapter\nA long paragraph."}
	batch := &codexBatch{client: f, cfg: &config.Config{CodexModel: "batch-model"}}
	result, err := batch.Transform(context.Background(), BatchRequest{SystemPrompt: "Narrate faithfully", Text: "book"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != f.response || !f.requests[0].Ephemeral || f.requests[0].Tools || f.requests[0].ThreadID != "" {
		t.Fatalf("batch contract lost: %+v", f.requests[0])
	}
}
