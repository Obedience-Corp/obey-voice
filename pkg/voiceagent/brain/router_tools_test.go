package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

func TestTieredWarmupAndLiveRequestsHaveIdenticalRoutingPrefix(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			workers := config.DefaultVoiceWorkers()
			workers.Mode = config.WorkerModeTiered
			host := "http://127.0.0.1:11434"
			workers.Tiers.Simple.Enabled = enabled
			workers.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "private-model", Billing: config.WorkerBillingLocal, EndpointRef: &host}
			cfg := &config.Config{VoiceWorkers: workers, VoiceToolsEnabled: false, VoiceRouterTools: "legacy", OllamaNumCtx: 8192, MaxHistory: 10, AgentName: "Samantha"}
			var count int
			var bodies [][]byte
			o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", cfg: cfg, fullSystemPrompt: "You are Samantha. Keep it short."}
			o.Warmup(context.Background())
			stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, WorkerTiers: WorkerTierAvailabilityForConfig(cfg)})
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Chunks {
			}
			if result := <-stream.Done; result.Err != nil {
				t.Fatal(result.Err)
			}
			if count != 3 {
				t.Fatalf("requests %d, want warmup, live routing, and chat answer", count)
			}
			type request struct {
				Messages []api.Message   `json:"messages"`
				Tools    json.RawMessage `json:"tools"`
			}
			var warm, live request
			if err := json.Unmarshal(bodies[0], &warm); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(bodies[1], &live); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(warm.Tools, live.Tools) || !bytes.Contains(warm.Tools, []byte(`"tier"`)) {
				t.Fatalf("warm/live routing schema mismatch\nwarm=%s\nlive=%s", warm.Tools, live.Tools)
			}
			if len(warm.Messages) == 0 || len(live.Messages) == 0 || warm.Messages[0].Content != live.Messages[0].Content {
				t.Fatal("warm/live system prefix mismatch")
			}
		})
	}
}

func TestLiveRouterToolsExcludeRunCommand(t *testing.T) {
	t.Parallel()
	tools := liveRouterTools()
	if hasTool(tools, "run_command") {
		t.Fatal("live router exposes run_command")
	}
	if !hasTool(tools, "route") {
		t.Fatal("live router missing route tool")
	}
	if len(tools) != 1 {
		t.Fatalf("live router tool count = %d, want 1", len(tools))
	}
}

func TestRouterToolsForSessionRouteOnly(t *testing.T) {
	t.Parallel()
	tools := RouterToolsForSession(RouterToolsRouteOnly, nil)
	if hasTool(tools, "run_command") || hasTool(tools, "list_files") {
		t.Fatalf("route_only must not expose worker tools: %#v", routerToolNames(tools))
	}
}

func TestTieredRouteSchemaAndMetadata(t *testing.T) {
	tiers := &WorkerTierAvailability{Simple: true, Complex: true}
	tools := liveRouterTools(tiers)
	route := tools[0].Function
	if len(route.Parameters.Required) != 2 || route.Parameters.Required[1] != "tier" {
		t.Fatalf("required = %#v, want kind,tier", route.Parameters.Required)
	}
	tier, ok := route.Parameters.Properties.Get("tier")
	if !ok || len(tier.Enum) != 3 {
		t.Fatalf("tier schema = %#v, want all three enum values", tier)
	}
	call := api.ToolCall{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{
		"kind": "spawn", "tier": "medium", "reason_code": "dependent_steps",
	})}}
	gotTier, gotReason := routeMetadata([]api.ToolCall{call})
	if gotTier != config.WorkerTierMedium || gotReason != "dependent_steps" {
		t.Fatalf("metadata = %q,%q", gotTier, gotReason)
	}
	invalidTier, _ := routeMetadata([]api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"tier": "bogus"})}}})
	if invalidTier != "" {
		t.Fatalf("invalid tier = %q, want empty", invalidTier)
	}
	conflict := []api.ToolCall{
		{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn", "tier": "simple", "reason_code": "bounded_action"})}},
		{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn", "tier": "complex", "reason_code": "broad_scope"})}},
	}
	if gotTier, gotReason := routeMetadata(conflict); gotTier != "" || gotReason != "" {
		t.Fatalf("conflicting metadata = %q,%q, want empty", gotTier, gotReason)
	}
}

func TestWorkerFactsPromptIsBoundedAndLabelFree(t *testing.T) {
	facts := make([]WorkerFact, 10)
	for i := range facts {
		facts[i] = WorkerFact{ID: "job", Tier: config.WorkerTierSimple, State: "queued"}
	}
	got := WorkerFactsPrompt(facts)
	if strings.Count(got, "job=") != 8 || strings.Contains(got, "provider") || strings.Contains(got, "model") {
		t.Fatalf("facts prompt = %q", got)
	}
	invalid := WorkerFactsPrompt([]WorkerFact{{ID: strings.Repeat("x", 129), Tier: config.WorkerTierSimple, State: "running"}, {ID: "ok", Tier: config.WorkerTierSimple, State: "invented"}})
	if invalid != "\n<active_work>\n</active_work>" {
		t.Fatalf("invalid facts prompt = %q", invalid)
	}
}

func TestOllamaHTTPRouteMetadataForEachTier(t *testing.T) {
	for _, want := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex} {
		t.Run(string(want), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"model":"m","message":{"tool_calls":[{"function":{"name":"route","arguments":{"kind":"spawn","tier":%q,"reason_code":"explicit_request"}}}]},"done":true}`+"\n", want)))
			}))
			t.Cleanup(srv.Close)
			base, _ := url.Parse(srv.URL)
			o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m", cfg: &config.Config{AgentName: "Samantha", OllamaNumCtx: 8192, VoiceToolsEnabled: true}, fullSystemPrompt: "prompt"}
			stream, err := o.ThinkStream(context.Background(), "do it", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, WorkerTiers: &WorkerTierAvailability{Simple: true, Medium: true, Complex: true}})
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Chunks {
			}
			result := <-stream.Done
			if result.Route != "spawn" || result.RouteTier != want || result.RouteReasonCode != "explicit_request" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestRouterToolsForSessionEmptyKeepsWorkerTools(t *testing.T) {
	t.Parallel()
	tools := RouterToolsForSession("", nil)
	if !hasTool(tools, "web_search") || !hasTool(tools, "run_command") {
		t.Fatalf("empty policy must keep worker tools: %#v", routerToolNames(tools))
	}
}

func TestLiveRouterDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy RouterToolsPolicy
		call   string
		kind   string
		want   string
	}{
		{name: "route spawn", policy: RouterToolsRouteOnly, call: "route", kind: "spawn", want: "spawn"},
		{name: "route narrate", policy: RouterToolsRouteOnly, call: "route", kind: "narrate", want: "narrate"},
		{name: "route chat stays in turn", policy: RouterToolsRouteOnly, call: "route", kind: "chat", want: ""},
		{name: "web_search becomes spawn", policy: RouterToolsRouteOnly, call: "web_search", want: "spawn"},
		{name: "run_command becomes spawn", policy: RouterToolsRouteOnly, call: "run_command", want: "spawn"},
		{name: "agent_delegate stays in turn", policy: RouterToolsRouteOnly, call: "agent_delegate", want: ""},
		{name: "agent_cancel stays in turn", policy: RouterToolsRouteOnly, call: "agent_cancel", want: ""},
		{name: "agent_delegations stays in turn", policy: RouterToolsRouteOnly, call: "agent_delegations", want: ""},
		{name: "legacy ignores worker tools", policy: RouterToolsLegacy, call: "web_search", want: ""},
		{name: "empty policy ignores worker tools", policy: "", call: "web_search", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{}
			if tt.kind != "" {
				args["kind"] = tt.kind
			}
			got := LiveRouterDecision(tt.policy, []api.ToolCall{{
				Function: api.ToolCallFunction{Name: tt.call, Arguments: mustArgs(args)},
			}})
			if got != tt.want {
				t.Fatalf("LiveRouterDecision = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRouterToolsForSessionLegacyKeepsWorkerTools(t *testing.T) {
	t.Parallel()
	tools := RouterToolsForSession(RouterToolsLegacy, nil)
	for _, name := range []string{"run_command", "list_files", "web_search"} {
		if !hasTool(tools, name) {
			t.Fatalf("legacy missing %q", name)
		}
	}
}

func TestNormalizeRouterToolsPolicyUnknownFailsClosed(t *testing.T) {
	t.Parallel()
	if got := NormalizeRouterToolsPolicy("bogus"); got != RouterToolsRouteOnly {
		t.Fatalf("unknown policy = %q, want route_only", got)
	}
}

func TestLiveSessionRejectsRunCommand(t *testing.T) {
	t.Parallel()
	sess := &toolSession{routerPolicy: RouterToolsRouteOnly}
	result := sess.execute(context.Background(), ".", api.ToolCall{
		Function: api.ToolCallFunction{
			Name:      "run_command",
			Arguments: mustArgs(map[string]any{"command": "ls"}),
		},
	})
	if !strings.Contains(result, "not available on the live router") {
		t.Fatalf("execute run_command = %q, want live-router rejection", result)
	}
}

func TestTieredLiveSessionRejectsHallucinatedDelegation(t *testing.T) {
	sess := &toolSession{routerPolicy: RouterToolsRouteOnly, workerTiers: &WorkerTierAvailability{Simple: true}}
	result := sess.execute(context.Background(), ".", api.ToolCall{Function: api.ToolCallFunction{Name: "agent_delegate"}})
	if !strings.Contains(result, "unavailable on the tiered live router") {
		t.Fatalf("execute agent_delegate = %q", result)
	}
}

func TestRouteToolInvalidKindFallsThroughToChat(t *testing.T) {
	t.Parallel()
	got := toolRoute(map[string]any{"kind": "fly-me-to-the-moon"})
	if !strings.Contains(got, `"route":"chat"`) {
		t.Fatalf("toolRoute invalid = %q, want chat", got)
	}
}

func routerToolNames(tools api.Tools) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Function.Name
	}
	return names
}

func TestRouteToolReportsSucceededOutcome(t *testing.T) {
	t.Parallel()
	var got ToolResult
	sess := &toolSession{routerPolicy: RouterToolsRouteOnly, onEnd: func(r ToolResult) { got = r }}
	result := sess.execute(context.Background(), ".", api.ToolCall{
		Function: api.ToolCallFunction{
			Name:      "route",
			Arguments: mustArgs(map[string]any{"kind": "chat"}),
		},
	})
	if !strings.Contains(result, `"route":"chat"`) {
		t.Fatalf("execute route = %q", result)
	}
	if got.Outcome == nil || got.Outcome.Status != events.ToolOutcomeSucceeded {
		t.Fatalf("route outcome = %+v, want succeeded", got.Outcome)
	}
	if got.Err != "" {
		t.Fatalf("route Err = %q, want empty", got.Err)
	}
}
