package netapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestWorkOutcomeReachesSocketWithIdentityAndFrozenTiming(t *testing.T) {
	bus := events.NewBus()
	runner := &scriptedRunner{runs: make(chan struct{}, 1)}
	_, addr, creds := startTestServer(t, runner, bus)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws := dialStream(t, ctx, addr, creds)
	writeControl(t, ctx, ws, controlMessage{Type: "text_input", Text: "connect"})
	select {
	case <-runner.runs:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	c := work.NewCoordinator(scope)
	key, err := c.Register(scope, work.Admission{Task: "check files", Origin: "agency", Provider: "ollama", Model: "original"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeFailed, Text: "Two files could not be read.", Elapsed: 3 * time.Second, Termination: "known"})
	if err != nil {
		t.Fatal(err)
	}
	bus.Emit(events.WorkStateChanged{Snapshot: c.Snapshot(scope)})
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		Type string        `json:"type"`
		Work work.Snapshot `json:"work"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "work_state_changed" || message.Work.Scope != scope || len(message.Work.Pending) != 1 {
		t.Fatalf("wire lost work state: %s", data)
	}
	got := message.Work.Pending[0]
	if got.Outcome.ResultID != result.Outcome.ResultID || got.Outcome.Status != work.OutcomeFailed || got.Outcome.Text != result.Outcome.Text || got.Outcome.Elapsed != 3*time.Second || got.Outcome.Model != "original" || got.State != work.Pending {
		t.Fatalf("wire changed outcome: %+v", got)
	}
}
