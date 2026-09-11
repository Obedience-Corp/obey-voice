package pipeline

import (
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestCanonicalTranscriptDoesNotCauseRepeatedAutoCompaction(t *testing.T) {
	b, err := brain.NewOllama(&config.Config{OllamaHost: ollamaDeltaServer(t, []string{"Samantha response-a1 remembers cobalt."}), OllamaModel: "m", AgentName: "Santa", MaxHistory: 20, PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := brain.CloseProvider(b); err != nil {
			t.Errorf("close provider: %v", err)
		}
	})
	var history []brain.Turn
	for range 5 {
		history = append(history, brain.Turn{Role: "user", Content: "remember cobalt"}, brain.Turn{Role: "assistant", Content: "cobalt", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "response-a1"})
	}
	if err := b.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	saves := 0
	p := &Pipeline{Brain: b, CompactPrompt: "Summarize preserving original persona names and source response IDs.", AutoCompactAfter: 4, OnTurn: func() { saves++ }}
	if err := p.autoCompactIfNeeded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.History(), history) || len(b.ContextHistory()) != 3 || saves != 1 {
		t.Fatalf("compaction state: canonical=%d context=%d saves=%d", len(b.History()), len(b.ContextHistory()), saves)
	}
	if err := p.autoCompactIfNeeded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatal("full transcript triggered another compaction without new input")
	}
}

func TestCompactTailPreservesCompleteToolExchangeOrDropsIt(t *testing.T) {
	group := []brain.Turn{{Role: "user", Content: "lookup"}, {Role: "assistant", Content: "checking", ToolCalls: []brain.TurnToolCall{{ID: "c1", Name: "lookup"}}}, {Role: "tool", ToolCallID: "c1", Content: "cobalt"}, {Role: "assistant", Content: "done", PersonaID: "santa", ResponseID: "r1"}}
	if got := compactTail(group); !reflect.DeepEqual(got, group) {
		t.Fatalf("valid native group changed: %+v", got)
	}
	// With two tool results the group exceeds the tail budget. Keep the
	// summarized facts, not an assistant call missing its result.
	group[1].ToolCalls = append(group[1].ToolCalls, brain.TurnToolCall{ID: "c2", Name: "lookup"})
	group = append(group[:3], brain.Turn{Role: "tool", ToolCallID: "c2", Content: "teapot"}, group[3])
	got := compactTail(group)
	calls, results := 0, 0
	for _, turn := range got {
		calls += len(turn.ToolCalls)
		if turn.Role == "tool" {
			results++
		}
	}
	if calls != results || (len(got) > 0 && got[0].Role != "user") {
		t.Fatalf("partial group retained: %+v", got)
	}
}
