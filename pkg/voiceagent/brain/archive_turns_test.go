package brain

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// routedTurns is n chat turns as the route-then-reply path stores them.
func routedTurns(n, questionBytes int) []Turn {
	var turns []Turn
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("route-%02d", i)
		turns = append(turns,
			Turn{Role: "user", Content: fmt.Sprintf("question-%02d %s", i, strings.Repeat("q", questionBytes))},
			Turn{Role: "assistant", ToolCalls: []TurnToolCall{{ID: id, Name: "route", Arguments: `{"kind":"chat"}`}}},
			Turn{Role: "tool", ToolName: "route", ToolCallID: id, Content: `{"route":"chat"}`},
			Turn{Role: "assistant", Content: fmt.Sprintf("answer-%02d", i)},
		)
	}
	return turns
}

// archivedTurns reads every archived note back as question/answer pairs and
// fails on anything else: a cut turn, an empty line, or route bookkeeping.
func archivedTurns(t *testing.T, o *OllamaBrain) []string {
	t.Helper()
	var questions []string
	for _, e := range o.ContinuityArchive() {
		if e.Kind != "turn_block" {
			t.Fatalf("archive entry of kind %q: %q", e.Kind, e.Summary)
		}
		lines := strings.Split(strings.TrimSuffix(e.Summary, "\n"), "\n")
		if len(lines)%2 != 0 {
			t.Fatalf("note does not hold whole turns: %q", e.Summary)
		}
		for i := 0; i < len(lines); i += 2 {
			q, a := lines[i], lines[i+1]
			if !strings.HasPrefix(q, "user: question-") || a != "assistant: answer-"+q[len("user: question-"):len("user: question-")+2] {
				t.Fatalf("archived turn reads %q / %q", q, a)
			}
			questions = append(questions, q[len("user: "):len("user: question-")+2])
		}
	}
	return questions
}

func TestEveryEvictionArchivesWholeChatTurns(t *testing.T) {
	for _, path := range []string{"turn budget", "context guard", "failed summary"} {
		t.Run(path, func(t *testing.T) {
			cfg := continuityConfig()
			o := &OllamaBrain{cfg: cfg, fullSystemPrompt: "You are Obey."}
			switch path {
			case "turn budget":
				o.LoadHistory(routedTurns(min(cfg.MaxHistory, archiveHistoryTurnsHigh)+1, 300))
				o.trimHistory()
			case "context guard":
				o.LoadHistory(routedTurns(12, 2400))
				o.ensureContextBudget("")
			case "failed summary":
				o.LoadHistory(routedTurns(12, 2400))
				o.TrimContinuity("next question")
			}
			kept := userTurns(o.history)
			got := archivedTurns(t, o)
			var want []string
			for i := 1; i <= len(got); i++ {
				want = append(want, fmt.Sprintf("question-%02d", i))
			}
			if len(got) == 0 || !slices.Equal(got, want) || !strings.HasPrefix(o.history[0].Content, fmt.Sprintf("question-%02d", len(got)+1)) {
				t.Fatalf("archived %v, kept %d turns starting %q", got, kept, o.history[0].Content)
			}
			if notes := len(o.ContinuityArchive()); notes >= len(got) && len(got) > 1 {
				t.Fatalf("%d turns took %d notes; whole turns should share notes", len(got), notes)
			}
		})
	}
}

// Route decisions never take archive entries of their own, so the archive
// holds hundreds of chat turns before it starts folding notes together.
func TestArchiveHoldsHundredsOfChatTurnsBeforeFolding(t *testing.T) {
	reply := alternate("That sounds like a good plan, and here is one more thought. ", "This sounds like a good plan, and here is one more thought. ", 3)
	fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, answer: reply}
	srv := fake.server(t)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: budgetConfig()}
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
	for n := 1; n <= 300; n++ {
		if turn := runRoutedTurn(t, o, fmt.Sprintf("Question number %03d about the garden?", n), opts); turn.result.Err != nil {
			t.Fatal(turn.result.Err)
		}
	}
	entries := o.ContinuityArchive()
	held := 0
	for _, e := range entries {
		if e.Kind != "turn_block" || strings.Contains(e.Summary, "route") || strings.Contains(e.Summary, "assistant: \n") {
			t.Fatalf("archive entry %q: %q", e.Kind, e.Summary)
		}
		held += strings.Count(e.Summary, "Question number")
	}
	if len(entries) >= 50 || held != 300-userTurns(o.history) {
		t.Fatalf("after 300 turns: %d notes holding %d turns, %d turns hot", len(entries), held, userTurns(o.history))
	}
}
