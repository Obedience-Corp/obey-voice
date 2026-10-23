package brain

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

func TestCodexReplayRetainsHistoricalRolesAndToolContext(t *testing.T) {
	c, f := codexFixture(t)
	c.cfg.WorkDir, c.cfg.WorkDirSource = t.TempDir(), "fixture"
	turns := []Turn{
		{Role: "user", Content: "Hey Santa, look up cobalt", RoutedInput: "look up cobalt", Speaker: "speaker-2"},
		{Role: "assistant", PersonaID: "santa", Content: "Checking", ToolCalls: []TurnToolCall{{ID: "call-1", Name: "lookup", Arguments: `{"key":"cobalt"}`}}},
		{Role: "tool", ToolCallID: "call-1", ToolName: "lookup", Content: "cobalt is blue"},
		{Role: "samantha", PersonaDisplayName: "Samantha", Content: "I prefer blue", ResponseID: "internal-response-id"},
		{Role: "assistant", Content: "An unattributed historical observation"},
	}
	c.LoadHistory(turns)
	if _, err := c.ThinkFull(context.Background(), "What did they find?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	r := f.requests[0]
	for _, want := range []string{`"speaker":"speaker-2"`, `Hey Santa, look up cobalt`, `"persona":"santa"`, `"tool_calls":[{"id":"call-1","name":"lookup"`, `"role":"tool","content":"cobalt is blue"`, `"tool_call_id":"call-1"`, `"persona":"Samantha"`, `"role":"assistant","content":"An unattributed historical observation"`, "Speak briefly."} {
		if !strings.Contains(r.Prompt, want) {
			t.Errorf("missing context %q in %s", want, r.Prompt)
		}
	}
	if strings.Contains(r.Prompt, "internal-response-id") || strings.Contains(r.Prompt, `"persona":"Obey"`) {
		t.Fatalf("replay invented ownership or leaked internal IDs: %s", r.Prompt)
	}
	if r.Cwd != c.cfg.WorkDir || r.System != c.systemPrompt || r.ThreadID != "" {
		t.Fatalf("workspace/persona grounding lost: %+v", r)
	}
}

func TestCodexContextWindowKeepsWholeExchangesAndSummary(t *testing.T) {
	c, _ := codexFixture(t)
	c.cfg.MaxHistory = 1
	seed := Turn{Role: "assistant", Content: "Shared summary: the project is cobalt."}
	exchange := []Turn{
		{Role: "user", Content: "Inspect the project"},
		{Role: "assistant", ToolCalls: []TurnToolCall{{ID: "c1", Name: "lookup"}}},
		{Role: "tool", ToolCallID: "c1", Content: "inspection result"},
		{Role: "assistant", Content: "Inspected", PersonaID: "helper"},
	}
	turns := append([]Turn{seed, {Role: "user", Content: "older"}, {Role: "assistant", Content: "older reply"}}, exchange...)
	c.LoadHistory(turns)
	if want := append([]Turn{seed}, exchange...); !reflect.DeepEqual(c.ContextHistory(), want) {
		t.Fatalf("context cut through an exchange or lost summary: %+v", c.ContextHistory())
	}
	if !reflect.DeepEqual(c.History(), turns) {
		t.Fatal("bounded context trimmed saved transcript")
	}
}

func TestCodexReseedPreservesCanonicalAndReplaysSummary(t *testing.T) {
	c, f := codexFixture(t)
	original := []Turn{{Role: "user", Content: "remember cobalt"}, {Role: "assistant", Content: "remembered", PersonaID: "santa"}}
	c.LoadHistory(original)
	c.sessionID = "pre-compact"
	seed := []Turn{{Role: "assistant", Content: "Shared summary: Santa remembers cobalt."}}
	// This is the same optional interface used by Pipeline.CompactConversation.
	var reseed interface{ ReseedContinuity([]Turn) } = c
	reseed.ReseedContinuity(seed)
	seed[0].Content = "mutated caller"
	if !reflect.DeepEqual(c.CanonicalHistory(), original) || c.sessionID != "" {
		t.Fatal("compaction replaced canonical transcript or kept stale native thread")
	}
	if _, err := c.ThinkFull(context.Background(), "Recall the project", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.requests[0].Prompt, "Shared summary: Santa remembers cobalt.") || strings.Contains(f.requests[0].Prompt, "mutated caller") || len(c.History()) != 4 {
		t.Fatalf("summary replay failed: %+v", f.requests[0])
	}
}

func TestCodexContinuityArchiveIsNotAliased(t *testing.T) {
	c, _ := codexFixture(t)
	snapshot := ContinuitySnapshot{Archive: []continuity.Entry{{Summary: "cobalt"}}}
	if err := c.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Archive[0].Summary = "mutated input"
	exported := c.ContinuitySnapshot()
	exported.Archive[0].Summary = "mutated export"
	if c.ContinuitySnapshot().Archive[0].Summary != "cobalt" {
		t.Fatal("persona transfer aliased continuity archive")
	}
}
