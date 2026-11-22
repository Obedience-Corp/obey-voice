package brain

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func listHere() []api.ToolCall {
	return []api.ToolCall{{Function: api.ToolCallFunction{Name: "list_files", Arguments: mustArgs(map[string]any{"path": "."})}}}
}

// A model often says a line, calls a tool, and says another line. Each line is
// its own thought. Run together they read "Let me look.Here it is." on screen
// and in the saved reply.
func TestTextFromSeparateToolRoundsIsSeparated(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", Content: "Let me look.", ToolCalls: listHere()},
		{Role: "assistant", ToolCalls: listHere()},
		{Role: "assistant", Content: "here is what I found.", ToolCalls: listHere()},
		{Role: "assistant", Content: "All done."},
	})
	o.workDir = t.TempDir()
	turn := runRoutedTurn(t, o, "What is in this folder?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect})
	if turn.result.Err != nil || len(*requests) != 4 {
		t.Fatalf("result=%+v requests=%d", turn.result, len(*requests))
	}
	// One break per boundary, however many silent tool rounds lie between.
	if want := "Let me look.\n\nhere is what I found.\n\nAll done."; turn.answer != want {
		t.Fatalf("streamed text = %q, want %q", turn.answer, want)
	}
	for _, said := range o.History() {
		if strings.Contains(said.Content, "\n\n") {
			t.Fatalf("the break was written into history: %+v", said)
		}
	}
}

// The break belongs to the text that follows it. A turn whose last words came
// before a tool call must not end on blank lines.
func TestTextBeforeTheLastToolRoundHasNoTrailingBreak(t *testing.T) {
	o, _ := imageClaimBrain(t, []api.Message{
		{Role: "assistant", Content: "Let me look.", ToolCalls: listHere()},
		{Role: "assistant", Content: "Let me look."},
	})
	o.workDir = t.TempDir()
	turn := runRoutedTurn(t, o, "What is in this folder?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect})
	if turn.result.Err != nil || turn.answer != "Let me look." {
		t.Fatalf("streamed text = %q, err = %v", turn.answer, turn.result.Err)
	}
}
