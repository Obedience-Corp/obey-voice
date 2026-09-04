package brain

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/ollama/ollama/api"
)

type delegationClientStub struct {
	started   chan struct{}
	completed bool
}

func (s *delegationClientStub) Probe(context.Context) error { return nil }
func (s *delegationClientStub) Start(context.Context, delegation.Request) (delegation.Snapshot, error) {
	close(s.started)
	return delegation.Snapshot{ID: "durable-id", State: "working"}, nil
}
func (s *delegationClientStub) Await(ctx context.Context, _ string) (delegation.Snapshot, error) {
	if s.completed {
		return delegation.Snapshot{ID: "durable-id", State: "completed", Text: "The task is finished."}, nil
	}
	<-ctx.Done()
	return delegation.Snapshot{}, ctx.Err()
}
func (s *delegationClientStub) Cancel(context.Context, string, string) (delegation.Snapshot, error) {
	return delegation.Snapshot{ID: "durable-id", State: "canceled"}, nil
}
func TestDelegationToolExposureAndAsyncAck(t *testing.T) {
	disabled := &toolSession{}
	for _, tool := range disabled.tools() {
		if strings.HasPrefix(tool.Function.Name, "agent_") {
			t.Fatal("delegation tool offered without runtime")
		}
	}
	call := api.ToolCall{Function: api.ToolCallFunction{Name: "agent_delegate"}}
	if got := disabled.execute(context.Background(), ".", call); !strings.Contains(got, "unavailable") {
		t.Fatal(got)
	}
	client := &delegationClientStub{started: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	sess := &toolSession{delegation: runtime}
	names := map[string]bool{}
	for _, tool := range sess.tools() {
		names[tool.Function.Name] = true
	}
	if !names["agent_delegate"] || !names["agent_cancel"] || !names["agent_delegations"] || names["agent_await"] {
		t.Fatalf("tools=%v", names)
	}
	call.Function.Arguments.Set("task", "Inspect the active workitems.")
	result := make(chan string, 1)
	go func() { result <- sess.execute(context.Background(), ".", call) }()
	select {
	case ack := <-result:
		if !strings.Contains(ack, "queued") || strings.Contains(ack, "Task completed") {
			t.Fatal(ack)
		}
	case <-time.After(time.Second):
		t.Fatal("tool blocked on worker")
	}
	<-client.started
	o := &OllamaBrain{delegation: runtime}
	if o.delegationForTurn(StreamOptions{}) != nil || o.delegationForTurn(StreamOptions{ToolsEnabled: true, OmitTurnInstruction: true}) != nil {
		t.Fatal("disabled/meta turn can delegate")
	}
	if o.delegationForTurn(StreamOptions{ToolsEnabled: true}) != runtime {
		t.Fatal("enabled turn missing runtime")
	}
}

type recordingProjector struct{ source string }

func (p *recordingProjector) AddDelegationNote(brief, source string) string {
	p.source = source
	return brief
}
func TestReadyDelegationSurvivesBudgetAndFailedDelivery(t *testing.T) {
	client := &delegationClientStub{started: make(chan struct{}), completed: true}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	ticket, err := runtime.Start("task")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := runtime.Pending(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no result")
		}
		time.Sleep(time.Millisecond)
	}
	o := &OllamaBrain{delegation: runtime, cfg: &config.Config{OllamaNumCtx: 2048}}
	o.history = []api.Message{{Role: "user", Content: strings.Repeat("old ", 3000)}, {Role: "assistant", Content: "old reply"}, {Role: "user", Content: "What did you find?"}}
	projector := &recordingProjector{}
	o.receiveProjectedDelegationResults(projector)
	o.ensureContextBudget("")
	if projector.source != "durable-id" {
		t.Fatal("missing durable provenance")
	}
	o.ackDelegationResults(nil)
	if _, ok := runtime.Pending(); !ok {
		t.Fatal("unsent/failed result was acknowledged")
	}
	// Reinjection after interrupted speech must not duplicate correlation IDs.
	o.receiveProjectedDelegationResults(projector)
	count := 0
	for _, m := range o.history {
		if m.ToolCallID == ticket+"-result" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("paired results=%d", count)
	}
	o.ackDelegationResults(o.buildMessages(""))
	if _, ok := runtime.Pending(); ok {
		t.Fatal("successfully included result remains pending")
	}
}
