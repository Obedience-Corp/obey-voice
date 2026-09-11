package voiceagent

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestExperimentalAdmissionRefusesPaidInjectedWorker(t *testing.T) {
	worker := &recordingInjectedWorker{reply: "must not run"}
	bus := events.NewBus()
	var refusal string
	events.Subscribe(bus, func(e events.Error) {
		if e.Stage == "job" {
			refusal = e.Message
		}
	})
	cfg := &config.Config{AgentName: "Test", VoiceWorkEnabled: true, VoiceWorkerProvider: "grok"}
	agent, cleanup, err := New(context.Background(), Options{Config: cfg, Events: bus, Brain: &stubBrain{}, Worker: worker, TextOnly: true, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := agent.EnableConversationWork("test-session", nil, cfg); err != nil {
		t.Fatal(err)
	}
	if err := agent.SendText(t.Context(), "search the web for rust"); err != nil {
		t.Fatal(err)
	}
	if calls, _ := worker.snapshot(); calls != 0 {
		t.Fatalf("paid worker ran %d times", calls)
	}
	if len(agent.WorkSnapshot().Jobs) != 0 || !strings.Contains(refusal, "paid") {
		t.Fatalf("refusal=%q state=%+v", refusal, agent.WorkSnapshot())
	}
}
