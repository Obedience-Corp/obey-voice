package pipeline

import (
	"context"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type continuityHistoryBrain struct {
	*fakeBrain
	snapshot brain.ContinuitySnapshot
}

func (b *continuityHistoryBrain) History() []brain.Turn {
	return append([]brain.Turn(nil), b.snapshot.Canonical...)
}

func (b *continuityHistoryBrain) ContinuitySnapshot() brain.ContinuitySnapshot {
	return b.snapshot
}

func (b *continuityHistoryBrain) ImportContinuity(snapshot brain.ContinuitySnapshot) error {
	b.snapshot = snapshot
	return nil
}

func TestCompleteLocalReplyPreservesCompactedContinuity(t *testing.T) {
	canonical := []brain.Turn{
		{Role: "user", Content: "earlier request"},
		{Role: "assistant", Content: "earlier answer"},
		{Role: "user", Content: "latest request"},
	}
	working := []brain.Turn{
		{Role: "assistant", Content: "summary of earlier exchange"},
		canonical[2],
	}
	archive := []continuity.Entry{{ID: "archive-1", Kind: "tool_block", Raw: "raw tool output"}}
	b := &continuityHistoryBrain{
		fakeBrain: &fakeBrain{},
		snapshot: brain.ContinuitySnapshot{
			Canonical:        canonical,
			Working:          working,
			Archive:          archive,
			ContextHighWater: 8192,
		},
	}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.SetOutputMuted(true)
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{ResponseID: "ack-1", PersonaID: "samantha"}

	if err := p.completeLocalReply(context.Background(), "Samantha, hello", "Hello.", metrics, p.newTurnConductor(metrics)); err != nil {
		t.Fatal(err)
	}

	if len(b.snapshot.Canonical) != len(canonical)+2 {
		t.Fatalf("canonical history = %#v, want full history plus acknowledgement", b.snapshot.Canonical)
	}
	if !reflect.DeepEqual(b.snapshot.Canonical[:len(canonical)], canonical) {
		t.Fatalf("canonical prefix changed: %#v", b.snapshot.Canonical)
	}
	if len(b.snapshot.Working) != len(working)+2 || !reflect.DeepEqual(b.snapshot.Working[:len(working)], working) {
		t.Fatalf("working view expanded or changed: %#v", b.snapshot.Working)
	}
	if b.snapshot.Canonical[len(canonical)].Role != "user" || b.snapshot.Canonical[len(canonical)].Content != "Samantha, hello" {
		t.Fatalf("local user turn = %#v", b.snapshot.Canonical[len(canonical)])
	}
	if got := b.snapshot.Canonical[len(b.snapshot.Canonical)-1]; got.Content != "Hello." || got.ResponseID != "ack-1" {
		t.Fatalf("local acknowledgement = %#v", got)
	}
	if !reflect.DeepEqual(b.snapshot.Archive, archive) || b.snapshot.ContextHighWater != 8192 {
		t.Fatalf("continuity metadata changed: archive=%#v highwater=%d", b.snapshot.Archive, b.snapshot.ContextHighWater)
	}
}

func TestAppendRoutedHistoryPreservesCompactedContinuity(t *testing.T) {
	canonical := []brain.Turn{
		{Role: "user", Content: "earlier request"},
		{Role: "assistant", Content: "earlier answer"},
	}
	working := []brain.Turn{{Role: "assistant", Content: "compacted summary"}}
	archive := []continuity.Entry{{ID: "archive-2", Kind: "turn_block", Summary: "older turns"}}
	b := &continuityHistoryBrain{
		fakeBrain: &fakeBrain{},
		snapshot: brain.ContinuitySnapshot{
			Canonical:        canonical,
			Working:          working,
			Archive:          archive,
			ContextHighWater: 16384,
		},
	}
	p := &Pipeline{Brain: b}
	metrics := newTurnMetrics()
	metrics.originalInput = "Samantha, create the report"
	metrics.historyAtAdmission = len(canonical)
	metrics.response = events.ResponseEnvelope{ResponseID: "route-1", PersonaID: "samantha"}

	if err := p.appendRoutedHistory(metrics, "create the report", "Working on it."); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(b.snapshot.Canonical[:len(canonical)], canonical) || len(b.snapshot.Canonical) != len(canonical)+2 {
		t.Fatalf("canonical history = %#v, want full history plus routed exchange", b.snapshot.Canonical)
	}
	if len(b.snapshot.Working) != len(working)+2 || !reflect.DeepEqual(b.snapshot.Working[:len(working)], working) {
		t.Fatalf("working view expanded or changed: %#v", b.snapshot.Working)
	}
	if got := b.snapshot.Canonical[len(canonical)]; got.RoutedInput != "create the report" {
		t.Fatalf("routed user turn = %#v", got)
	}
	if got := b.snapshot.Canonical[len(b.snapshot.Canonical)-1]; got.Content != "Working on it." || got.ResponseID != "route-1" {
		t.Fatalf("routed acknowledgement = %#v", got)
	}
	if !reflect.DeepEqual(b.snapshot.Archive, archive) || b.snapshot.ContextHighWater != 16384 {
		t.Fatalf("continuity metadata changed: archive=%#v highwater=%d", b.snapshot.Archive, b.snapshot.ContextHighWater)
	}
}
