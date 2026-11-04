package brain

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"reflect"
	"testing"
)

func TestPersonaContextRejectsDivergentTranscriptAndClonesMemory(t *testing.T) {
	canonical := []Turn{{Role: "user", Content: "shared request"}}
	state := CapturePersonaContext(ContinuitySnapshot{Canonical: canonical, Working: []Turn{{Role: "assistant", Content: "private seed"}}, Archive: []continuity.Entry{{ID: "private"}}, ContextHighWater: 8192})
	restored := state.Reconcile([]Turn{{Role: "user", Content: "different conversation"}})
	if len(restored.Archive) != 0 || restored.ContextHighWater != 0 || restored.Working[0].Content != "different conversation" {
		t.Fatal("divergent transcript inherited private memory")
	}
	contexts := PersonaContexts{"alpha": state}
	copy := contexts.Clone()
	copy["alpha"].Working[0].Content = "changed"
	if reflect.DeepEqual(copy, contexts) {
		t.Fatal("snapshot clone aliases working context")
	}
}

func TestPersonaContextRefreshesSeenTurnDispositionWithoutLosingPrivateMemory(t *testing.T) {
	canonical := []Turn{{Role: "assistant", Content: "authentic response", ResponseID: "reply-1"}}
	state := CapturePersonaContext(ContinuitySnapshot{Canonical: canonical, Working: canonical, Archive: []continuity.Entry{{ID: "own-memory"}}})
	updated := deepCopyTurns(canonical)
	updated[0].Disposition = "interrupted"
	got := state.Reconcile(updated)
	if len(got.Archive) != 1 || got.Working[0].Disposition != "interrupted" || got.Working[0].Content != canonical[0].Content {
		t.Fatal("seen-turn update discarded private memory or authentic text")
	}
}

func TestPersonaContextKeepsToolTurnsSharingResponseIdentityDistinct(t *testing.T) {
	turns := []Turn{
		{Role: "assistant", ResponseID: "response-1", ToolCalls: []TurnToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"fixture"}`}}},
		{Role: "tool", ResponseID: "response-1", ToolCallID: "call-1", ToolName: "read_file", Content: "fixture result"},
		{Role: "assistant", ResponseID: "response-1", Content: "final fixture answer"},
	}
	state := CapturePersonaContext(ContinuitySnapshot{Canonical: turns, Working: turns})
	got := state.Reconcile(turns)
	if !reflect.DeepEqual(got.Working, turns) {
		t.Fatalf("same-response tool group was collapsed: %+v", got.Working)
	}
	if got.Working[0].ToolCalls[0].ID != got.Working[1].ToolCallID {
		t.Fatal("tool pairing changed")
	}
}

func TestPersonaContextKeepsBoundedToolProjection(t *testing.T) {
	canonical := []Turn{{Role: "tool", ToolCallID: "call-1", ToolName: "read_file", Content: "full canonical result"}}
	working := deepCopyTurns(canonical)
	working[0].Content = "bounded persona note"
	got := CapturePersonaContext(ContinuitySnapshot{Canonical: canonical, Working: working}).Reconcile(canonical)
	if got.Working[0].Content != "bounded persona note" || got.Canonical[0].Content != "full canonical result" {
		t.Fatal("reconciliation expanded bounded tool memory")
	}
}
