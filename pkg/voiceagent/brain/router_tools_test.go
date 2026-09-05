package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

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
