package brain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/ollama/ollama/api"
)

func TestOllamaFailedThinkFullRestoresWorkingView(t *testing.T) {
	o := &OllamaBrain{client: ollamaFailStub(t, true), model: "m", cfg: &config.Config{MaxHistory: 10}}
	seed := []Turn{{Role: "user", Content: "old"}, {Role: "assistant", Content: "reply"}}
	o.LoadHistory(seed)
	want := deepCopyTurns(o.working)

	if _, err := o.ThinkFull(t.Context(), "new", StreamOptions{ToolsEnabled: true}); err == nil {
		t.Fatal("ThinkFull() error = nil")
	}
	if !reflect.DeepEqual(o.working, want) {
		t.Fatalf("working view after failed ThinkFull = %#v, want %#v", o.working, want)
	}
}

func TestOllamaTransientThinkFullDoesNotLeakIntoWorkingView(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", cfg: &config.Config{MaxHistory: 10}}
	o.LoadHistory([]Turn{{Role: "user", Content: "old"}})
	wantCanonical := o.CanonicalHistory()
	wantWorking := deepCopyTurns(o.working)

	if _, err := o.ThinkFull(t.Context(), "summarize", StreamOptions{TransientHistory: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.CanonicalHistory(), wantCanonical) || !reflect.DeepEqual(o.working, wantWorking) {
		t.Fatalf("transient turn leaked: canonical=%#v working=%#v", o.CanonicalHistory(), o.working)
	}
}

func TestOllamaContinuationCompactionUpdatesWorkingView(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", cfg: continuityConfig()}
	o.LoadHistory([]Turn{
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "old reply"},
		{Role: "user", Content: "latest"},
	})
	canonical := o.CanonicalHistory()
	if err := o.compactForContinuation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(o.working) != len(o.history) || o.working[0].Content != "Shared conversation notes: hi" {
		t.Fatalf("working view = %#v, native history = %#v", o.working, o.history)
	}
	if !reflect.DeepEqual(o.CanonicalHistory(), canonical) {
		t.Fatal("continuation compaction changed canonical transcript")
	}
}

func TestOllamaBudgetTrimUpdatesWorkingView(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{MaxHistory: 10}}
	turns := []Turn{
		{Role: "user", Content: strings.Repeat("old ", 200)},
		{Role: "assistant", Content: strings.Repeat("reply ", 200)},
		{Role: "user", Content: "middle"},
		{Role: "assistant", Content: "middle reply"},
		{Role: "user", Content: "latest"},
		{Role: "assistant", Content: "answer"},
	}
	o.LoadHistory(turns)
	if !o.dropOldestBeforeLastUser("", 1) {
		t.Fatal("budget trim did not trim")
	}
	if len(o.history) != len(o.working) || len(o.working) == 0 || o.working[0].Content != "latest" {
		t.Fatalf("working view = %#v, native history = %#v", o.working, o.history)
	}
}

func TestOllamaContinuityTransferCopiesArchiveAndBoundedHighWater(t *testing.T) {
	source := &OllamaBrain{cfg: &config.Config{OllamaNumCtx: 8192, ContinuityNumCtxCeil: 32768}}
	source.archive.Add(continuity.Entry{Kind: "tool", Summary: "remember WI-transfer"})
	source.setContextHighWater(32768)
	snapshot := source.ContinuitySnapshot()

	target := &OllamaBrain{cfg: &config.Config{OllamaNumCtx: 4096, ContinuityNumCtxCeil: 8192}}
	if err := target.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target.ContinuityNotes(), "WI-transfer") {
		t.Fatal("archive recall was not transferred")
	}
	if got := target.effectiveNumCtx(); got != 8192 {
		t.Fatalf("context high-water = %d, want receiver ceiling 8192", got)
	}
}

func TestOllamaImportContinuityPreservesExplicitEmptyWorkingView(t *testing.T) {
	target := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha"}}
	snapshot := ContinuitySnapshot{
		Canonical: []Turn{{Role: "user", Content: "durable"}},
		Working:   []Turn{},
	}
	if err := target.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(target.history) != 0 || len(target.working) != 0 {
		t.Fatalf("empty working view expanded: history=%#v working=%#v", target.history, target.working)
	}
}

func TestOllamaDelegationRedeliveryRemovesAllWorkingViewPairs(t *testing.T) {
	call := api.ToolCall{ID: "job-result", Function: api.ToolCallFunction{Name: "agent_delegations"}}
	o := &OllamaBrain{
		history: []api.Message{
			{Role: "user", Content: "latest"},
			{Role: "assistant", ToolCalls: []api.ToolCall{call}},
			{Role: "tool", ToolCallID: call.ID, Content: "old note"},
		},
		canonical: []Turn{
			{Role: "user", Content: "latest"},
			{Role: "assistant", ToolCalls: []TurnToolCall{{ID: call.ID}}},
			{Role: "tool", ToolCallID: call.ID, Content: "old note"},
		},
		working: []Turn{
			{Role: "user", Content: "latest"},
			{Role: "assistant", ToolCalls: []TurnToolCall{{ID: call.ID}}},
			{Role: "tool", ToolCallID: call.ID, Content: "old note"},
		},
	}
	o.removeDelegationPair(call.ID)
	if len(o.history) != 1 || len(o.canonical) != 1 || len(o.working) != 1 {
		t.Fatalf("delegation pair leaked: history=%#v canonical=%#v working=%#v", o.history, o.canonical, o.working)
	}
}
