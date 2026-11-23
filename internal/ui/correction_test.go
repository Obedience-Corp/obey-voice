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

func TestReportLinesFollowTheConversation(t *testing.T) {
	bus := events.NewBus()
	var out bytes.Buffer
	u := NewWithOutput(bus, "Assistant", &out)
	u.SetWorkSnapshot(func() work.Snapshot { return work.Snapshot{} })
	current := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c2"}
	u.SetConversationScope(func() work.WorkScope { return current })
	bus.Emit(events.JobReport{Scope: current, ID: "job-1", Checkpoint: "read 2 files", Answer: true})
	bus.Emit(events.JobReport{Scope: current, ID: "job-2", Checkpoint: "ran 3 commands"})
	bus.Emit(events.JobReport{Scope: work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c1"}, ID: "job-0", Checkpoint: "read 1 file", Answer: true})
	text := out.String()
	if !strings.Contains(text, "job-1 report requested · done so far: read 2 files\n") || !strings.Contains(text, "job-2 report requested · done so far: ran 3 commands · the work continues") || strings.Contains(text, "job-0") {
		t.Fatalf("output=%q", text)
	}
}
