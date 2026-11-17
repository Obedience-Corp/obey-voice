package brain

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/ollama/ollama/api"
)

// toolTurns is n turns of three tool rounds each: eight stored messages.
func toolTurns(n int) []Turn {
	var turns []Turn
	for i := 1; i <= n; i++ {
		turns = append(turns, Turn{Role: "user", Content: fmt.Sprintf("task-%02d", i)})
		for r := range 3 {
			id := fmt.Sprintf("call-%02d-%d", i, r)
			turns = append(turns,
				Turn{Role: "assistant", ToolCalls: []TurnToolCall{{ID: id, Name: "read_file", Arguments: "{}"}}},
				Turn{Role: "tool", ToolName: "read_file", ToolCallID: id, Content: "contents"})
		}
		turns = append(turns, Turn{Role: "assistant", Content: fmt.Sprintf("done-%02d", i)})
	}
	return turns
}

// A reply the host writes itself, such as a spawn acknowledgement, must keep
// the window below the message count that starts a compaction, as a reply the
// model writes does.
func TestHostWrittenReplyKeepsTheMessageBound(t *testing.T) {
	cfg := budgetConfig()
	o := &OllamaBrain{cfg: cfg}
	o.LoadHistory(toolTurns(10))
	o.trimHistory()
	if got := len(o.ContextHistory()); got != cfg.MaxHistory*2 {
		t.Fatalf("fixture holds %d messages, want %d", got, cfg.MaxHistory*2)
	}
	snapshot := o.ContinuitySnapshot()
	snapshot.Working = append(snapshot.Working, Turn{Role: "user", Content: "look that up"}, Turn{Role: "assistant", Content: "I'll work on that and report back."})
	snapshot.Canonical = append(snapshot.Canonical, snapshot.Working[len(snapshot.Working)-2:]...)
	if err := o.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	o.BoundHostTurns(2)
	if got := len(o.ContextHistory()); got > cfg.MaxHistory*2 {
		t.Fatalf("host-written reply left %d messages, past the compaction trigger at %d", got, cfg.MaxHistory*2)
	}
}

// A transient request, such as a compaction summary, leaves history and the
// archive as it found them.
func TestTransientRequestDoesNotArchive(t *testing.T) {
	fake := &jsonRouteFake{answer: "Summary."}
	srv := fake.server(t)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: budgetConfig()}
	o.LoadHistory(routedTurns(archiveHistoryTurnsHigh, 10))
	before := len(o.history)
	if _, err := o.ThinkFull(t.Context(), "Summarize the conversation.", StreamOptions{TransientHistory: true, OmitTurnInstruction: true}); err != nil {
		t.Fatal(err)
	}
	if len(o.history) != before || len(o.ContinuityArchive()) != 0 {
		t.Fatalf("history %d -> %d messages, %d archive notes", before, len(o.history), len(o.ContinuityArchive()))
	}
}
