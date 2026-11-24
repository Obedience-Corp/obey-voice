package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// With the work inbox off, a correction to a job of this conversation is
// shown, and one from a cleared conversation is not.
func TestCorrectionLinesFollowTheConversation(t *testing.T) {
	bus := events.NewBus()
	var out bytes.Buffer
	u := NewWithOutput(bus, "Assistant", &out)
	u.SetWorkSnapshot(func() work.Snapshot { return work.Snapshot{} })
	current := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c2"}
	u.SetConversationScope(func() work.WorkScope { return current })
	bus.Emit(events.JobCorrection{Scope: current, ID: "job-1", Revision: 1, State: "included"})
	bus.Emit(events.JobCorrection{Scope: work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c1"}, ID: "job-0", Revision: 1, State: "included"})
	text := out.String()
	if !strings.Contains(text, "job-1 correction included") || strings.Contains(text, "job-0") {
		t.Fatalf("output=%q", text)
	}
}
