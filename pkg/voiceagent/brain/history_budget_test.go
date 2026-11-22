package brain

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func budgetConfig() *config.Config {
	return &config.Config{MaxHistory: 40, OllamaNumCtx: 32768, ContinuityProjectionEnabled: true, ContinuityArchiveEnabled: true, ContinuitySoftLimitRatio: 0.8}
}

// retainedTurns is the number of user turns kept after n plain chat turns:
// everything up to the high mark, then the low mark plus turns since the
// last trim.
func retainedTurns(n int) int {
	// Turns kept after turn n: all of them up to 12, then back to 6 after
	// turns 13 and 20.
	return []int{0,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12,
		6, 7, 8, 9, 10, 11, 12,
		6, 7, 8, 9, 10, 11, 12,
	}[n]
}

func turnBlocks(o *OllamaBrain) []string {
	var blocks []string
	for _, e := range o.ContinuityArchive() {
		if e.Kind == "turn_block" {
			blocks = append(blocks, e.Summary)
		}
	}
	return blocks
}

func archivedInputs(block string) []string {
	var inputs []string
	for line := range strings.SplitSeq(block, "\n") {
		if input, ok := strings.CutPrefix(line, "user: "); ok {
			inputs = append(inputs, input)
		}
	}
	return inputs
}

func assertWholeGroups(t *testing.T, history []api.Message) {
	t.Helper()
	if len(history) > 0 && history[0].Role != "user" {
		t.Fatalf("window opens on %s, not a user message", history[0].Role)
	}
	calls := map[string]bool{}
	for _, m := range history {
		for _, tc := range m.ToolCalls {
			calls[tc.ID+"/"+tc.Function.Name] = true
		}
		if m.Role == "tool" && !calls[m.ToolCallID+"/"+m.ToolName] {
			t.Fatalf("tool result %q kept without its call", m.ToolName)
		}
	}
}

func assertWorkingAligned(t *testing.T, o *OllamaBrain) {
	t.Helper()
	if len(o.working) != len(o.history) {
		t.Fatalf("working=%d history=%d", len(o.working), len(o.history))
	}
	for i, m := range o.history {
		content := withoutImageNotes(m.Content, o.working[i].Attachments)
		if o.working[i].Role != m.Role || (m.Role == "user" && o.working[i].Content != content) {
			t.Fatalf("working[%d]=%s %q, history %s %q", i, o.working[i].Role, o.working[i].Content, m.Role, m.Content)
		}
	}
}

// spokenReply is a realistic spoken answer, long enough that one archive note
// cannot hold every turn a trim evicts.
var spokenReply = strings.Repeat("That sounds like a good plan, and here is one more thought on it. ", 5)

func TestRoutedChatHistoryIsBudgetedInUserTurns(t *testing.T) {
	const turns = 26
	for _, tc := range []struct {
		name    string
		json    bool
		perTurn int
	}{
		{"route tool", false, 4},
		{"json route", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, decide: fixedDecision(`{"kind":"chat"}`), answer: spokenReply}
			srv := fake.server(t)
			base, _ := url.Parse(srv.URL)
			o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: budgetConfig()}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: tc.json}

			var trims []int
			var dropped []string
			for n := 1; n <= turns; n++ {
				before := len(turnBlocks(o))
				input := fmt.Sprintf("turn-%02d", n)
				if turn := runRoutedTurn(t, o, input, opts); turn.result.Err != nil || strings.TrimSpace(turn.answer) != strings.TrimSpace(spokenReply) {
					t.Fatalf("turn %d: answer=%q err=%v", n, turn.answer, turn.result.Err)
				}
				if got, want := userTurns(o.history), retainedTurns(n); got != want {
					t.Fatalf("after turn %d: retained %d user turns, want %d", n, got, want)
				}
				if got, want := len(o.history), retainedTurns(n)*tc.perTurn; got != want {
					t.Fatalf("after turn %d: %d messages, want %d", n, got, want)
				}
				if newest := o.history[len(o.history)-tc.perTurn]; newest.Role != "user" || !strings.HasPrefix(newest.Content, input) {
					t.Fatalf("after turn %d: newest turn opens with %s %q", n, newest.Role, newest.Content)
				}
				assertWholeGroups(t, o.history)
				assertWorkingAligned(t, o)
				blocks := turnBlocks(o)
				if len(blocks) == before {
					continue
				}
				trims = append(trims, n)
				var got []string
				for _, block := range blocks[before:] {
					got = append(got, archivedInputs(block)...)
				}
				var want []string
				for k := len(dropped) + 1; k <= n-userTurns(o.history); k++ {
					want = append(want, fmt.Sprintf("turn-%02d", k))
				}
				if !slices.Equal(got, want) {
					t.Fatalf("turn %d archived %v, want %v", n, got, want)
				}
				dropped = append(dropped, got...)
			}
			if want := []int{13, 20}; !slices.Equal(trims, want) {
				t.Fatalf("trimmed after turns %v, want %v", trims, want)
			}
			var want []string
			for k := 1; k <= turns-userTurns(o.history); k++ {
				want = append(want, fmt.Sprintf("turn-%02d", k))
			}
			if !slices.Equal(dropped, want) {
				t.Fatalf("archive holds %v, want every dropped turn once: %v", dropped, want)
			}
			if got := len(o.CanonicalHistory()); got != turns*tc.perTurn {
				t.Fatalf("canonical history has %d messages, want %d", got, turns*tc.perTurn)
			}
		})
	}
}

// Between trims each request extends the previous one: everything before the
// previous turn's user message (which loses its per-turn reminder) is
// unchanged, so the server's prefix cache can reuse it.
func TestRoutedChatPrefixIsStableBetweenTrims(t *testing.T) {
	fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, answer: "Noted."}
	srv := fake.server(t)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: budgetConfig()}
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}

	var routes, replies []api.ChatRequest
	for n := 1; n <= archiveHistoryTurnsHigh+1; n++ {
		before := len(fake.sent())
		if turn := runRoutedTurn(t, o, fmt.Sprintf("turn-%02d", n), opts); turn.result.Err != nil {
			t.Fatal(turn.result.Err)
		}
		sent := fake.sent()[before:]
		if len(sent) != 2 || !hasTool(sent[0].Tools, "route") {
			t.Fatalf("turn %d sent %d requests", n, len(sent))
		}
		routes = append(routes, sent[0])
		replies = append(replies, sent[1])
	}
	// The last turn's requests were built before its trim, so all
	// archiveHistoryTurnsHigh+1 route calls share one growing prefix, and so do
	// the reply calls.
	for name, requests := range map[string][]api.ChatRequest{"route": routes, "reply": replies} {
		for i := 1; i < len(requests); i++ {
			prev, next := requests[i-1].Messages, requests[i].Messages
			p := lastUserIndex(prev)
			if p < 1 || len(next) <= p {
				t.Fatalf("%s request %d: unexpected shape", name, i)
			}
			if a, b := jsonOf(t, prev[:p]), jsonOf(t, next[:p]); a != b {
				t.Fatalf("%s request %d changed its first %d messages:\nprev %s\nnext %s", name, i+1, p, a, b)
			}
		}
	}
	if userTurns(o.history) != archiveHistoryTurnsLow {
		t.Fatalf("turn %d did not trim to the low mark: %d turns", archiveHistoryTurnsHigh+1, userTurns(o.history))
	}
}

func toolTurn(n, rounds int) []api.Message {
	msgs := []api.Message{{Role: "user", Content: fmt.Sprintf("task-%02d", n)}}
	for r := range rounds {
		id := fmt.Sprintf("call-%02d-%d", n, r)
		msgs = append(msgs,
			api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: id, Function: api.ToolCallFunction{Name: "read_file"}}}},
			api.Message{Role: "tool", ToolName: "read_file", ToolCallID: id, Content: "contents"})
	}
	return append(msgs, api.Message{Role: "assistant", Content: fmt.Sprintf("done-%02d", n)})
}

// Real tool work is conversation: its rounds stay with their turn, and the
// message marks still bound a tool-heavy window below the count that would
// start a compaction.
func TestToolHeavyHistoryKeepsMessageBound(t *testing.T) {
	cfg := budgetConfig()
	o := &OllamaBrain{cfg: cfg}
	for n := 1; n <= 11; n++ {
		o.history = append(o.history, toolTurn(n, 3)...)
		o.trimHistory()
		if len(o.history) > cfg.MaxHistory*2 {
			t.Fatalf("after turn %d: %d messages exceed %d", n, len(o.history), cfg.MaxHistory*2)
		}
		assertWholeGroups(t, o.history)
	}
	// Ten 8-message turns reach 80; the eleventh trims to at most MaxHistory
	// messages, which is five whole turns.
	if got := userTurns(o.history); got != 5 || len(o.history) != 40 || o.history[0].Content != "task-07" {
		t.Fatalf("kept %d turns in %d messages from %q", got, len(o.history), o.history[0].Content)
	}

}

func TestHistoryTurnMarksFollowMaxHistory(t *testing.T) {
	routed := []api.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "r", Function: api.ToolCallFunction{Name: "route"}}}},
		{Role: "tool", ToolName: "route", ToolCallID: "r", Content: "chat"},
		{Role: "assistant", Content: "a"},
	}
	plain := []api.Message{routed[0], routed[3]}
	for _, tc := range []struct {
		maxHistory int
		turn       []api.Message
		high, low  int
	}{
		{40, plain, 12, 6},
		{12, plain, 12, 6},
		{8, plain, 8, 4},
		{4, plain, 4, 2},
		// Four stored messages a turn: at 40 the turn marks bind; below 24
		// the message marks do (24 then 12 messages at 12, 16 then 8 at 8).
		{40, routed, 12, 6},
		{12, routed, 6, 3},
		{8, routed, 4, 2},
	} {
		cfg := budgetConfig()
		cfg.MaxHistory = tc.maxHistory
		o := &OllamaBrain{cfg: cfg}
		trimmedAt, keptAfter := 0, 0
		for n := 1; n <= 20 && trimmedAt == 0; n++ {
			o.history = append(o.history, tc.turn...)
			o.trimHistory()
			if userTurns(o.history) < n {
				trimmedAt, keptAfter = n, userTurns(o.history)
			}
		}
		if trimmedAt != tc.high+1 || keptAfter != tc.low {
			t.Errorf("max_history=%d, %d-message turns: trimmed after turn %d to %d turns, want after %d to %d", tc.maxHistory, len(tc.turn), trimmedAt, keptAfter, tc.high+1, tc.low)
		}
	}
}
