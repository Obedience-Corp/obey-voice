package personaruntime

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestClaudeReceivesOnlyResultsOfJobsItStarted(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.work = true
	c.start("ember", nil, nil)
	// Jobs record the persona of the config the Agency admits them with.
	requireJobPersona := func() {
		t.Helper()
		if got, want := c.p.Agency.WorkerMigrationConfig().ActivePersona, c.selector.Snapshot().PersonaID; got != want || got == "" {
			t.Fatalf("jobs would be recorded for persona %q while %q answers", got, want)
		}
	}
	requireJobPersona()
	coordinator := c.p.WorkCoordinator()
	scope := coordinator.Scope()
	key, err := coordinator.Register(scope, work.Admission{JobID: "ember-job", Task: "Read my private tax notes", Origin: "agency", Config: map[string]string{"persona": "ember"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Started(key); err != nil {
		t.Fatal(err)
	}
	c.say("switch to Mira")
	requireJobPersona()
	if _, err := coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "The tax refund is 1234."}); err != nil {
		t.Fatal(err)
	}
	if c.p.BackgroundPending() {
		t.Fatal("Ember's result is offered for delivery while Mira on Claude is answering")
	}
	c.say("What do you think?")
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "1234", "tax notes")
	}
	if pending := coordinator.Pending(coordinator.Scope()); len(pending) != 1 {
		t.Fatalf("Ember's result did not stay pending: %+v", pending)
	}

	own, err := coordinator.Register(scope, work.Admission{JobID: "mira-job", Task: "Check the build", Origin: "agency", Config: map[string]string{"persona": "mira"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(own, work.Outcome{Scope: own.Scope, JobID: own.JobID, Status: work.OutcomeSucceeded, Text: "The build is green."}); err != nil {
		t.Fatal(err)
	}
	if !c.p.BackgroundPending() {
		t.Fatal("Mira's own result is not offered to Mira")
	}
	c.say("And the build?")
	requireContains(t, lastClaudeRequest(t, c.log).Prompt, "The build is green.")
	requireOmits(t, lastClaudeRequest(t, c.log).Prompt, "1234")

	c.say("switch to Ember", "Any news?", "Anything else?")
	requireJobPersona()
	delivered := 0
	for _, request := range c.ollamaRequests() {
		var sent strings.Builder
		for _, m := range request {
			sent.WriteString(m.Content + "\n")
		}
		if strings.Contains(sent.String(), "The tax refund is 1234.") && strings.Contains(sent.String(), "result_id") {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("Ember's result was delivered %d times, want once", delivered)
	}
	for _, r := range coordinator.Pending(coordinator.Scope()) {
		if r.State == work.Pending || r.State == work.Ready {
			t.Fatalf("a result is still waiting: %+v", r)
		}
	}
}

func TestLoadHistoryGivesAPersonaOnClaudeOnlyItsShare(t *testing.T) {
	other := []brain.Turn{
		{Role: "user", Content: "Another conversation: the safe code is 4711."},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "other"},
	}
	held := func(c *claudeConversation) []string {
		var lines []string
		for _, turn := range c.p.Brain.History() {
			lines = append(lines, turn.Content)
		}
		return lines
	}
	c := newClaudeConversation(t, 20)
	c.selector.LoadHistory(other)
	if got := held(c); len(got) != 0 {
		t.Fatalf("a persona never addressed holds %q", got)
	}
	c.say("Note the door is teal.")
	c.selector.LoadHistory(c.selector.Transcript())
	if got := held(c); len(got) != 2 || got[0] != "Note the door is teal." {
		t.Fatalf("the persona's own share = %q", got)
	}
	c.selector.LoadHistory(other)
	if got := held(c); len(got) != 0 {
		t.Fatalf("another conversation was loaded: %q", got)
	}
}

func TestJobStartedOnALocalBackendIsNotDeliveredAfterThePersonaMovesToClaude(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.work = true
	c.start("ember", nil, nil)
	coordinator := c.p.WorkCoordinator()
	scope := coordinator.Scope()
	register := func(id, backend, text string) {
		t.Helper()
		key, err := coordinator.Register(scope, work.Admission{JobID: id, Task: "task " + id, Origin: "agency", Config: map[string]string{"persona": "ember", "brain": backend}})
		if err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Started(key); err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	register("local-job", "ollama", "The private tax refund is 1234.")

	ember := testProfile("ember", "Ember", "ember")
	ember.Brain.Provider, ember.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, ember)
	c.say("Hello.")
	if c.p.BackgroundPending() {
		t.Fatal("a result of a job started on Ollama is offered to Ember on Claude")
	}
	c.say("Any news?")
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "1234")
	}

	register("claude-job", "claude", "The build is green.")
	if !c.p.BackgroundPending() {
		t.Fatal("a result of a job Ember started while on Claude is not offered to it")
	}
	c.say("And the build?")
	requireContains(t, lastClaudeRequest(t, c.log).Prompt, "The build is green.")
	requireOmits(t, lastClaudeRequest(t, c.log).Prompt, "1234")
}
