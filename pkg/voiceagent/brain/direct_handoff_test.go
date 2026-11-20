package brain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ollama/ollama/api"
)

func TestDirectWorkerOwnsInitialToolBatch(t *testing.T) {
	for _, tool := range []string{backgroundWorkTool, "web_search", "fetch_url"} {
		t.Run(tool, func(t *testing.T) {
			dir := t.TempDir()
			write := api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "result", "content": "foreground"})}}
			handoff := api.ToolCall{Function: api.ToolCallFunction{Name: tool}}
			o, requests := imageClaimBrain(t, []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{write, handoff}}})
			o.workDir = dir
			turn := runRoutedTurn(t, o, "Can you get a new photo from the internet of a dog?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, BackgroundWorker: true})
			if turn.result.Err != nil || turn.result.Route != "spawn" || len(*requests) != 1 || !hasTool((*requests)[0].Tools, backgroundWorkTool) {
				t.Fatalf("handoff: %+v; requests=%d", turn.result, len(*requests))
			}
			if _, err := os.Stat(filepath.Join(dir, "result")); !os.IsNotExist(err) {
				t.Fatalf("foreground executed part of the worker's batch: %v", err)
			}
			noRouteExchangeInHistory(t, o)
		})
	}
}

func TestDirectWorkerCannotRestartPartiallyExecutedTurn(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "result", "content": "done"})}}}},
		{Role: "assistant", Content: "Done."},
	})
	o.workDir = t.TempDir()
	turn := runRoutedTurn(t, o, "Save the note.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, BackgroundWorker: true})
	if turn.result.Err != nil || turn.result.Route != "" || len(*requests) != 2 || hasTool((*requests)[1].Tools, backgroundWorkTool) {
		t.Fatalf("unexpected handoff after a write: %+v", turn.result)
	}
	data, err := os.ReadFile(filepath.Join(o.workDir, "result"))
	if err != nil || string(data) != "done" {
		t.Fatalf("inline write: %q %v", data, err)
	}
}

func TestDirectLookupWithoutWorkerStaysInForeground(t *testing.T) {
	// An empty URL exercises the real tool without issuing a network request.
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "fetch_url"}}}},
		{Role: "assistant", Content: "Which address should I open?"},
	})
	turn := runRoutedTurn(t, o, "Can you open the link?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect})
	if turn.result.Err != nil || turn.result.Route != "" || len(*requests) != 2 || hasTool((*requests)[0].Tools, backgroundWorkTool) ||
		len(turn.tools) != 1 || turn.tools[0].Name != "fetch_url" {
		t.Fatalf("lookup without a worker: %+v, tool events: %+v", turn.result, turn.tools)
	}
}

func TestDirectWorkerTakesOverAfterLocalInspection(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "list_files", Arguments: mustArgs(map[string]any{"path": "."})}}}},
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "web_search"}}}},
	})
	o.workDir = t.TempDir()
	turn := runRoutedTurn(t, o, "Get a new photo of a dog from the internet.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, BackgroundWorker: true})
	if turn.result.Err != nil || turn.result.Route != "spawn" || len(*requests) != 2 {
		t.Fatalf("handoff after inspection: %+v", turn.result)
	}
	history := o.History()
	if len(history) != 3 || history[0].Role != "user" || history[2].ToolName != "list_files" {
		t.Fatalf("foreground inspection history: %+v", history)
	}
}
