package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
)

func TestThinkFullPipesAPromptTooLargeForOneArgument(t *testing.T) {
	fake := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "Summary.", SessionID: "sess-1"}}}
	b := newTestBrain(fake)
	b.sessionID = "sess-1"
	turns := make([]Turn, 0, 16)
	for range 8 {
		turns = append(turns, Turn{Role: "user", Content: strings.Repeat("cobalt ", 4000)}, Turn{Role: "assistant", Content: "Noted.", PersonaID: "rex", PersonaDisplayName: "Rex"})
	}
	request := "Summarize everything. " + strings.Repeat("teal ", 20000)
	if _, err := b.ThinkFull(context.Background(), request, StreamOptions{TransientHistory: true, OmitTurnInstruction: true, CatchUp: &SessionCatchUp{Turns: turns}}); err != nil {
		t.Fatal(err)
	}
	for _, inline := range fake.runPrompts {
		if len(inline) >= 128<<10 {
			t.Fatalf("a %d byte prompt was passed as one argument", len(inline))
		}
	}
	if len(fake.stdinPrompts) != 1 || !strings.Contains(fake.stdinPrompts[0], "Summarize everything.") || !strings.Contains(fake.stdinPrompts[0], "cobalt") {
		t.Fatalf("the prompt did not reach the CLI on stdin: %d piped", len(fake.stdinPrompts))
	}
}

func TestThinkFullKeepsASmallPromptInline(t *testing.T) {
	fake := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "Hi.", SessionID: "sess-1"}}}
	b := newTestBrain(fake)
	if _, err := b.ThinkFull(context.Background(), "Hello.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.stdinPrompts) != 0 || len(fake.runPrompts) != 1 || !strings.Contains(fake.runPrompts[0], "Hello.") {
		t.Fatalf("inline = %d, piped = %d", len(fake.runPrompts), len(fake.stdinPrompts))
	}
}
