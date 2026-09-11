package brain

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/ollama/ollama/api"
)

func TestImportPreservesLiveDelegationAndArchive(t *testing.T) {
	client := &delegationClientStub{started: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	o := &OllamaBrain{cfg: &config.Config{ContinuityArchiveEnabled: true, ContinuityProjectionEnabled: true}}
	if err := o.AttachDelegation(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start("keep original work"); err != nil {
		t.Fatal(err)
	}
	<-client.started
	o.AddContinuityNote("original archive", continuity.Provenance{ToolName: "original"})
	before := o.ContinuityArchive()
	status := runtime.Status()
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "same conversation"}, {Role: "assistant", ToolCalls: []TurnToolCall{{ID: "c1", Name: "lookup", Arguments: `{}`}}}, {Role: "tool", ToolCallID: "c1", ToolName: "lookup", Content: strings.Repeat("large raw result ", 200)}}); err != nil {
		t.Fatal(err)
	}
	if o.delegation != runtime || !reflect.DeepEqual(o.ContinuityArchive(), before) || runtime.Status() != status {
		t.Fatal("import changed archive or live work")
	}
}

func TestToolProjectionRetainsRawCanonicalResult(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{ContinuityProjectionEnabled: true, ContinuityArchiveEnabled: true}}
	raw := strings.Repeat("original evidence ", 1000)
	call := api.ToolCall{ID: "lookup-1", Function: api.ToolCallFunction{Name: "lookup", Arguments: api.NewToolCallFunctionArguments()}}
	o.appendProjectedToolResult(call, raw)
	if o.History()[0].Content != raw || len(o.history[0].Content) >= len(raw) || o.history[0].ToolCallID != "lookup-1" {
		t.Fatal("canonical tool content lost or projection unbounded")
	}
}

func TestTransientSummaryDoesNotEnterCanonicalHistory(t *testing.T) {
	o := &OllamaBrain{client: ollamaStreamStub(t, []string{`{"message":{"role":"assistant","content":"Samantha response-a1 remembered cobalt."},"done":true}`}), model: "m", cfg: &config.Config{MaxHistory: 10}}
	seed := []Turn{{Role: "user", Content: "remember cobalt"}, {Role: "assistant", Content: "cobalt", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "response-a1"}}
	if err := o.ImportHistory(seed); err != nil {
		t.Fatal(err)
	}
	native := append([]api.Message(nil), o.history...)
	summary, err := o.ThinkFull(t.Context(), "summarize", StreamOptions{OmitTurnInstruction: true, TransientHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.History(), seed) || !reflect.DeepEqual(o.history, native) {
		t.Fatal("internal summary polluted history")
	}
	o.ReseedContinuity([]Turn{{Role: "assistant", Content: summary}})
	if !reflect.DeepEqual(o.History(), seed) || len(o.ContextHistory()) != 1 {
		t.Fatal("reseed replaced canonical transcript")
	}
}

func TestHistoryWindowKeepsWholeUserAndToolGroups(t *testing.T) {
	turns := []Turn{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old reply"}, {Role: "user", Content: "lookup"}, {Role: "assistant", Content: "checking", ToolCalls: []TurnToolCall{{ID: "a", Name: "lookup"}, {ID: "b", Name: "lookup"}}}, {Role: "tool", ToolCallID: "a", Content: "a"}, {Role: "tool", ToolCallID: "b", Content: "b"}, {Role: "assistant", Content: "done"}, {Role: "user", Content: "next"}, {Role: "assistant", Content: "reply"}}
	for size := 1; size <= len(turns); size++ {
		window := HistoryWindow(turns, size)
		if len(window) > 0 && window[0].Role != "user" {
			t.Fatalf("size %d starts on %s", size, window[0].Role)
		}
		calls, results := 0, 0
		for _, turn := range window {
			calls += len(turn.ToolCalls)
			if turn.Role == "tool" {
				results++
			}
		}
		if calls != results {
			t.Fatalf("size %d orphaned tool group: %+v", size, window)
		}
	}
}
