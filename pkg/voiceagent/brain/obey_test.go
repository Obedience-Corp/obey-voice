package brain

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type obeyRunFunc func(context.Context, string, *obey.RunOptions, func(obey.Event) error) (*obey.Result, error)

func (f obeyRunFunc) run(ctx context.Context, prompt string, opts *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
	return f(ctx, prompt, opts, emit)
}
func obeyTestBrain(f obeyRunFunc) *ObeyBrain {
	return &ObeyBrain{client: f, cfg: &config.Config{ObeyProvider: "configured-provider", ObeyModel: "configured-model", ObeyCamp: "camp", ObeyToolPolicy: "daemon", WorkDir: "/workspace", WorkDirSource: "fixture", MaxHistory: 2, AgentName: "Ada"}, systemPrompt: "persona instructions", turnInstruction: "voice instructions"}
}
func TestObeyStreamingAndFinalOnly(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "final-only", true: "live"}[live], func(t *testing.T) {
			b := obeyTestBrain(func(ctx context.Context, prompt string, opts *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
				if opts.Provider != "configured-provider" || opts.Model != "configured-model" || opts.Camp != "camp" || opts.WorkingDirectory != "/workspace" || opts.Mode != "discussion" {
					t.Errorf("bad options: %+v", opts)
				}
				if !strings.Contains(prompt, "persona instructions") || !strings.Contains(prompt, "voice instructions") || !strings.Contains(prompt, "hello") {
					t.Fatal(prompt)
				}
				if err := emit(obey.Event{Delta: &obey.MessageDelta{ContentKind: "reasoning", Text: "SECRET"}}); err != nil {
					return nil, err
				}
				if live {
					for _, text := range []string{"Ada: Hello", " there."} {
						if err := emit(obey.Event{Delta: &obey.MessageDelta{ContentKind: "assistant", Text: text}}); err != nil {
							return nil, err
						}
					}
				}
				return &obey.Result{Text: "Ada: Hello there."}, nil
			})
			stream, err := b.ThinkStream(context.Background(), "hello", StreamOptions{Speaker: "speaker-2", Identity: &TurnIdentity{PersonaID: "ada", ResponseID: "r1"}})
			if err != nil {
				t.Fatal(err)
			}
			var spoken strings.Builder
			for s := range stream.Chunks {
				spoken.WriteString(s)
			}
			if r := <-stream.Done; r.Err != nil {
				t.Fatal(r.Err)
			}
			if spoken.String() != "Hello there." {
				t.Fatalf("spoken = %q", spoken.String())
			}
			h := b.History()
			if len(h) != 2 || h[0].Speaker != "speaker-2" || h[1].ResponseID != "r1" {
				t.Fatalf("history = %+v", h)
			}
		})
	}
}
func TestObeyFailedTurnsNeverCommitOrRetry(t *testing.T) {
	failure := errors.New("daemon unavailable")
	for _, tc := range []struct {
		name   string
		result *obey.Result
		err    error
	}{
		{"failure", &obey.Result{Text: "partial"}, failure}, {"empty", &obey.Result{}, nil}, {"nil", nil, nil},
		{"cancellation", nil, context.Canceled}, {"deadline", nil, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := obeyTestBrain(func(context.Context, string, *obey.RunOptions, func(obey.Event) error) (*obey.Result, error) {
				calls++
				return tc.result, tc.err
			})
			b.LoadHistory([]Turn{{Role: "user", Content: "saved"}})
			before := b.History()
			_, err := b.ThinkFull(context.Background(), "new", StreamOptions{})
			if err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost cause: %v", err)
			}
			if !reflect.DeepEqual(before, b.History()) {
				t.Fatal("failed turn changed history")
			}
		})
	}
}
func TestObeyToolPolicyAndApproval(t *testing.T) {
	calls := 0
	b := obeyTestBrain(func(_ context.Context, _ string, _ *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
		calls++
		return nil, emit(obey.Event{Approval: &obey.Approval{ToolName: "shell"}})
	})
	b.cfg.ObeyToolPolicy = "strict"
	if _, err := b.ThinkFull(context.Background(), "hello", StreamOptions{}); err == nil || calls != 0 {
		t.Fatal("tools-off request reached daemon")
	}
	if _, err := b.ThinkStream(context.Background(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}); err == nil || calls != 0 {
		t.Fatal("route-only request reached daemon")
	}
	if _, err := b.ThinkFull(context.Background(), "work", StreamOptions{ToolsEnabled: true}); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("approval error=%v", err)
	}
	if len(b.History()) != 0 {
		t.Fatal("approval committed a turn")
	}
}
func TestObeyTransientContinuationAndFreshHistory(t *testing.T) {
	var prompts []string
	b := obeyTestBrain(func(_ context.Context, p string, _ *obey.RunOptions, _ func(obey.Event) error) (*obey.Result, error) {
		prompts = append(prompts, p)
		return &obey.Result{Text: "Done."}, nil
	})
	b.LoadHistory([]Turn{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer", PersonaDisplayName: "Other"}})
	b.ReseedContinuity([]Turn{{Role: "assistant", Content: "compact seed"}})
	before := b.ContinuitySnapshot()
	if _, err := b.ThinkFull(context.Background(), "summarize", StreamOptions{TransientHistory: true, OmitTurnInstruction: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, b.ContinuitySnapshot()) {
		t.Fatal("transient turn modified history")
	}
	if strings.Contains(prompts[0], "voice instructions") || strings.Contains(prompts[0], "old answer") || !strings.Contains(prompts[0], "compact seed") {
		t.Fatal(prompts[0])
	}
	if _, err := b.ThinkFull(context.Background(), "continue", StreamOptions{AgentContinuation: true}); err != nil {
		t.Fatal(err)
	}
	if len(b.History()) != 3 || b.History()[2].Role != "assistant" {
		t.Fatal("fabricated user turn")
	}
	fresh := b.Fresh().(*ObeyBrain)
	if len(fresh.History()) != 0 || len(fresh.ContextHistory()) != 0 {
		t.Fatal("worker inherited history")
	}
	restored := b.Fresh().(*ObeyBrain)
	if err := restored.ImportContinuity(b.ContinuitySnapshot()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.History(), restored.History()) || !reflect.DeepEqual(b.ContextHistory(), restored.ContextHistory()) {
		t.Fatal("continuity lost")
	}
	h := restored.History()
	h[0].Content = "mutated"
	if restored.History()[0].Content == "mutated" {
		t.Fatal("history aliases caller")
	}
}
func TestObeyBatchIsHistoryFree(t *testing.T) {
	b := obeyTestBrain(func(_ context.Context, p string, _ *obey.RunOptions, _ func(obey.Event) error) (*obey.Result, error) {
		if strings.Contains(p, "persona instructions") || !strings.Contains(p, "rewrite") || !strings.Contains(p, "source") {
			t.Fatal(p)
		}
		return &obey.Result{Text: "transformed"}, nil
	})
	result, err := (&obeyBatch{brain: b}).Transform(context.Background(), BatchRequest{SystemPrompt: "rewrite", Text: "source"})
	if err != nil || result.Text != "transformed" || result.Provider != "obey" || len(b.History()) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestObeyFactoryRejectsMissingConfiguration(t *testing.T) {
	for _, cfg := range []*config.Config{{BrainProvider: "obey"}, {BrainProvider: "obey", ObeyProvider: "ollama"}} {
		if _, err := NewProvider(cfg); err == nil {
			t.Fatal("missing config accepted")
		}
		if _, err := NewBatchProvider(cfg); err == nil {
			t.Fatal("missing batch config accepted")
		}
	}
}

func TestObeyToolEventsUsageAndCleanupWarning(t *testing.T) {
	complete, success := true, true
	b := obeyTestBrain(func(_ context.Context, _ string, _ *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
		for _, ev := range []obey.Event{
			{ToolCall: &obey.ToolCall{CallID: "call-1", Name: "read_file", Arguments: "private input"}},
			{ToolResult: &obey.ToolResult{CallID: "call-1", Content: "private output", Success: &success}},
		} {
			if err := emit(ev); err != nil {
				return nil, err
			}
		}
		return &obey.Result{Text: "Done.", Usage: obey.Usage{Complete: &complete, InputTokens: 10, OutputTokens: 2}, Cleanup: obey.CleanupStopFailed, Session: obey.SessionRef{SessionID: "owned"}}, nil
	})
	var start ToolCall
	var end ToolResult
	var usage WorkerUsage
	var warning string
	result, err := b.ThinkFull(t.Context(), "read", StreamOptions{
		OnToolStart: func(c ToolCall) { start = c }, OnToolEnd: func(r ToolResult) { end = r },
		OnWorkerUsage: func(u WorkerUsage) { usage = u }, OnPromptWarn: func(s string) { warning = s },
	})
	if err != nil || result != "Done." {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if start.ID != "call-1" || start.Kind != KindRead || end.ID != start.ID || end.Outcome == nil || end.Outcome.Status != "succeeded" {
		t.Fatalf("tool events: %+v %+v", start, end)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 10 || usage.OutputTokens == nil || *usage.OutputTokens != 2 || !strings.Contains(warning, "owned") {
		t.Fatal("usage or cleanup warning lost")
	}
	reportObeyUsage(obey.Usage{}, StreamOptions{OnWorkerUsage: func(u WorkerUsage) { usage = u }})
	if usage.InputTokens != nil || usage.OutputTokens != nil {
		t.Fatal("missing usage became zero")
	}
}

func TestObeyCancelBlockedStream(t *testing.T) {
	b := obeyTestBrain(func(_ context.Context, _ string, _ *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
		for i := 0; i < 100; i++ {
			if err := emit(obey.Event{Delta: &obey.MessageDelta{ContentKind: "assistant", Text: "a long reply "}}); err != nil {
				return nil, err
			}
		}
		return &obey.Result{}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := b.ThinkStream(ctx, "hello", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case result := <-stream.Done:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatal(result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled producer did not stop")
	}
	if len(b.History()) != 0 {
		t.Fatal("canceled stream committed history")
	}
}
