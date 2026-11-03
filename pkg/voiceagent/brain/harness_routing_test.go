package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

const harnessSpawn = `{"kind":"spawn","tier":"simple","reason_code":"bounded_action"}`

func TestHarnessRoutingAdaptersDoNotResumeOrMutateTalkingSession(t *testing.T) {
	req := LiveRouteRequest{Text: "What's the weather today?", WorkerTiers: &WorkerTierAvailability{Simple: true}}
	check := func(t *testing.T, b LiveRouteClassifier) {
		t.Helper()
		r, err := b.ClassifyLiveRoute(t.Context(), req)
		if err != nil || r.Route != "spawn" || r.RouteTier != config.WorkerTierSimple || r.RouteReasonCode != "bounded_action" {
			t.Fatalf("route=%+v err=%v", r, err)
		}
	}
	t.Run("codex", func(t *testing.T) {
		b, f := codexFixture(t)
		b.cfg.PromptsDir = t.TempDir()
		b.LoadHistory([]Turn{{Role: "user", Content: "Denver"}})
		b.sessionID = "talking"
		before := b.ContinuitySnapshot()
		f.response = harnessSpawn
		check(t, b)
		r := f.requests[0]
		if r.ThreadID != "" || r.Tools || !r.Ephemeral || r.Model != b.cfg.CodexModel || b.sessionID != "talking" || !reflect.DeepEqual(before, b.ContinuitySnapshot()) {
			t.Fatalf("routing changed the conversation: req=%+v history=%+v", r, b.History())
		}
	})
	t.Run("claude", func(t *testing.T) {
		f := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: harnessSpawn}}}
		b := newTestBrain(f)
		b.cfg.PromptsDir, b.sessionID = t.TempDir(), "talking"
		check(t, b)
		o := f.runOpts[0]
		if o.ResumeID != "" || !o.NoSessionPersistence || len(o.Tools) != 1 || o.Tools[0] != "" || o.PermissionMode != claude.PermissionModeDefault || b.sessionID != "talking" || len(b.History()) != 0 {
			t.Fatalf("unsafe routing options %+v", o)
		}
	})
	t.Run("cursor", func(t *testing.T) {
		f := &fakeCursorRunner{result: &cursor.AskResult{Result: harnessSpawn}}
		b := newTestCursorBrain(f)
		b.cfg.PromptsDir, b.cfg.CursorModel, b.sessionID = t.TempDir(), "fixture-model", "talking"
		check(t, b)
		o := f.gotOpts
		if o.Resume != "" || o.Mode != cursor.ModeAsk || o.Model != "fixture-model" || b.sessionID != "talking" || len(b.History()) != 0 {
			t.Fatalf("unsafe routing options %+v", o)
		}
	})
	t.Run("grok", func(t *testing.T) {
		f := &fakeGrokClient{fullResults: []*grok.GrokResult{{Text: harnessSpawn}}}
		b := newTestGrokBrain(f)
		b.cfg.PromptsDir, b.sessionID = t.TempDir(), "talking"
		check(t, b)
		o := f.runOpts[0]
		if o.ResumeID != "" || len(o.AllowedTools) != 1 || o.AllowedTools[0] != "" || o.AllowDangerousMode || b.sessionID != "talking" || len(b.History()) != 0 {
			t.Fatalf("unsafe routing options %+v", o)
		}
	})
	t.Run("muse-spark", func(t *testing.T) {
		f := &fakeMuse{result: &muse.Result{Text: harnessSpawn, Terminal: "completed"}}
		b := testMuse(f)
		b.cfg.PromptsDir = t.TempDir()
		check(t, b)
		o := f.opts
		if o.Preset != "empty" || !o.DisableWebTools || !o.DisableWrite || !o.DisableShell || !o.NoSessionLog || len(b.History()) != 0 {
			t.Fatalf("unsafe routing options %+v", o)
		}
	})
	t.Run("obey", func(t *testing.T) {
		b := obeyTestBrain(func(_ context.Context, prompt string, o *obey.RunOptions, _ func(obey.Event) error) (*obey.Result, error) {
			if o.Mode != "discussion" || !strings.Contains(prompt, "latest_user_request") || strings.Contains(prompt, "persona instructions") {
				t.Fatalf("unsafe routing request: %+v %s", o, prompt)
			}
			return &obey.Result{Text: harnessSpawn}, nil
		})
		b.cfg.PromptsDir = t.TempDir()
		check(t, b)
		if len(b.History()) != 0 {
			t.Fatal("routing was recorded as conversation")
		}
		b.cfg.ObeyToolPolicy = ""
		if _, err := b.ClassifyLiveRoute(t.Context(), req); err == nil {
			t.Fatal("bypassed daemon policy")
		}
	})
}

func TestHarnessRouteValidationAndContext(t *testing.T) {
	cfg := &config.Config{PromptsDir: t.TempDir()}
	for _, tc := range []struct {
		name, text string
		tiers      *WorkerTierAvailability
		fail       bool
	}{
		{"legacy spawn", `{"kind":"spawn"}`, nil, false},
		{"chat", `{"kind":"chat"}`, nil, false},
		{"tiered", harnessSpawn, &WorkerTierAvailability{Simple: true}, false},
		{"no tier", `{"kind":"spawn"}`, &WorkerTierAvailability{Simple: true}, true},
		{"invented tier", `{"kind":"spawn","tier":"enormous"}`, &WorkerTierAvailability{Simple: true}, true},
		{"prose", "I cannot search the web.", nil, true},
		{"unknown field", `{"kind":"spawn","command":"rm"}`, nil, true},
		{"null", `{"kind":null}`, nil, true},
		{"oversize", strings.Repeat(" ", 4097), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := classifyHarnessRoute(t.Context(), cfg, LiveRouteRequest{Text: "weather", WorkerTiers: tc.tiers}, func(context.Context, BatchRequest) (string, error) { return tc.text, nil })
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v fail=%t", err, tc.fail)
			}
		})
	}
	history := []Turn{{Role: "user", Content: "Use Denver, Colorado."}}
	for range 12 {
		history = append(history, Turn{Role: "assistant", Content: "Conversation context."})
	}
	_, err := classifyHarnessRoute(t.Context(), cfg, LiveRouteRequest{Text: "What about tomorrow?", Conversation: history}, func(ctx context.Context, req BatchRequest) (string, error) {
		if !strings.Contains(req.Text, "Denver, Colorado") || !strings.Contains(req.Text, "What about tomorrow?") || strings.Contains(req.SystemPrompt, "{route_") || !strings.Contains(req.SystemPrompt, `"kind"`) {
			t.Fatalf("missing context/schema: %+v", req)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded classifier")
		}
		return `{"kind":"spawn"}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = classifyHarnessRoute(ctx, cfg, LiveRouteRequest{}, func(context.Context, BatchRequest) (string, error) { t.Fatal("canceled inference ran"); return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHarnessRoutingPromptOverrideAndMalformedFallback(t *testing.T) {
	cfg := &config.Config{PromptsDir: t.TempDir()}
	dir := filepath.Join(cfg.PromptsDir, "system")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "harness-routing-chat.yaml")
	valid := "schema: obey-voice.prompt.v1\nprompt:\n  name: harness-routing-chat\n  kind: system\n  system_prompt: 'Override available={workers_available}'\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	opts := StreamOptions{HarnessRouting: &HarnessRouting{WorkersAvailable: true}}
	if got := harnessRoutingContext(cfg, opts); !strings.Contains(got, "Override available=true") {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte("malformed: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if got := harnessRoutingContext(cfg, opts); !strings.Contains(got, "Workers available: true") {
		t.Fatal(got)
	}
}
