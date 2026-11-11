package personaruntime

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestJobStartedOnALocalBackendIsDeliveredWhenThePersonaReturnsToIt(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.work = true
	c.start("ember", nil, nil)
	coordinator := c.p.WorkCoordinator()
	scope := coordinator.Scope()
	key, err := coordinator.Register(scope, work.Admission{JobID: "local-job", Task: "Read my tax notes", Origin: "agency", Config: map[string]string{"persona": "ember", "brain": "ollama"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Started(key); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "The tax refund is 1234."}); err != nil {
		t.Fatal(err)
	}

	onClaude := testProfile("ember", "Ember", "ember")
	onClaude.Brain.Provider, onClaude.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, onClaude)
	c.say("Hello.")
	if c.p.BackgroundPending() {
		t.Fatal("a result of a job started on Ollama is offered to Ember on Claude")
	}

	onOllama := testProfile("ember", "Ember", "ember")
	onOllama.Brain.Model = ""
	writeTestProfile(t, c.catalog.Dir, onOllama)
	c.say("Back again.")
	if got := c.p.Agency.WorkerMigrationConfig().BrainProvider; got == "claude" {
		t.Fatalf("Ember is still on %q", got)
	}
	delivered := false
	for _, request := range c.ollamaRequests() {
		for _, m := range request {
			if strings.Contains(m.Content, "The tax refund is 1234.") {
				delivered = true
			}
		}
	}
	if !delivered {
		t.Fatal("the withheld result never reached Ember on Ollama")
	}
	for _, r := range coordinator.Pending(coordinator.Scope()) {
		if r.State == work.Pending || r.State == work.Ready {
			t.Fatalf("the result is still waiting: %+v", r)
		}
	}
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "1234")
	}
}
