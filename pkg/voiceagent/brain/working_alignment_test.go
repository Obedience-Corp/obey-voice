package brain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestContinuitySanitizedPrefixAndEmptyWindowStayAligned(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "jarvis", AgentName: "Jarvis", MaxHistory: 1}}
	original := []Turn{
		{Role: "assistant", PersonaID: "jarvis"},
		{Role: "tool", PersonaID: "jarvis", Content: "orphan"},
		{Role: "user", Content: "old request"},
		{Role: "assistant", PersonaID: "jarvis", Content: "old answer"},
		{Role: "user", Content: "new request"},
		{Role: "assistant", PersonaID: "jarvis", Content: "new answer"},
	}
	o.LoadHistory(original)
	if len(o.working) != len(o.history) || len(o.working) != 4 {
		t.Fatalf("working=%+v native=%+v", o.working, o.history)
	}
	o.trimHistory()
	snapshot := o.ContinuitySnapshot()
	if snapshot.Working == nil || len(snapshot.Working) != len(o.history) {
		t.Fatalf("empty working view lost: %+v", snapshot)
	}
	next := &OllamaBrain{cfg: &config.Config{ActivePersona: "santa", AgentName: "Santa"}}
	if err := next.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(next.history) != 0 || !reflect.DeepEqual(original, next.History()) {
		t.Fatal("switch expanded empty view or lost canonical history")
	}
}

func TestContinuityTransferKeepsShrunkForeignToolBody(t *testing.T) {
	original := []Turn{
		{Role: "user", Content: "check this"},
		{Role: "assistant", PersonaID: "jarvis", PersonaDisplayName: "Jarvis", ToolCalls: []TurnToolCall{{ID: "call", Name: "lookup", Arguments: "{}"}}},
		{Role: "tool", PersonaID: "jarvis", PersonaDisplayName: "Jarvis", ToolName: "lookup", ToolCallID: "call", Content: strings.Repeat("raw result ", 1000)},
	}
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "santa", AgentName: "Santa", MaxHistory: 20}}
	o.LoadHistory(original)
	if !o.shrinkToolsToTokenLimit("", 300) {
		t.Fatal("fixture did not shrink")
	}
	snapshot := o.ContinuitySnapshot()
	if len(snapshot.Working[2].Content) >= len(original[2].Content) {
		t.Fatal("working source restored raw tool body")
	}
	next := &OllamaBrain{cfg: &config.Config{ActivePersona: "jarvis", AgentName: "Jarvis", MaxHistory: 20}}
	if err := next.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	if next.history[2].Role != "tool" || strings.Contains(next.history[2].Content, "Jarvis tool") || len(next.history[2].Content) > 1200 {
		t.Fatalf("tool was relabeled or expanded: %+v", next.history[2])
	}
	if !reflect.DeepEqual(original, next.History()) {
		t.Fatal("canonical evidence was shortened")
	}
}
