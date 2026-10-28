package tui

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestResumedGroupTranscriptPreservesEachPersona(t *testing.T) {
	m := newConversation("Santa")
	m.agentName = "Santa"
	m.seedTranscript([]brain.Turn{
		{Role: "user", Content: "Everyone, thoughts?"},
		{Role: "assistant", Content: "A local prototype helps.", PersonaID: "jarvis", PersonaDisplayName: "Jarvis"},
		{Role: "assistant", Content: "Check deployment parity.", PersonaID: "samantha", PersonaDisplayName: "Samantha"},
	})
	names := []string{}
	for _, item := range m.items {
		if item.kind == chatItemAgent {
			names = append(names, item.name)
		}
	}
	if len(names) != 2 || names[0] != "Jarvis" || names[1] != "Samantha" {
		t.Fatalf("restored attribution=%v", names)
	}
	m.handleEvent(events.DiscussionUpdated{Snapshot: discussion.Snapshot{Phase: "checking"}})
	m.handleEvent(events.DiscussionUpdated{Snapshot: discussion.Snapshot{Phase: "ended", Reason: "yielded"}})
	if m.status != "Group discussion ended" {
		t.Fatalf("terminal status=%q", m.status)
	}
}
