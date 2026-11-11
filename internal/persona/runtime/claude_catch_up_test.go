package personaruntime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func lastClaudeRequest(t *testing.T, log string) claudeRequest {
	t.Helper()
	requests := readClaudeRequests(t, log)
	if len(requests) == 0 {
		t.Fatal("no claude requests")
	}
	return requests[len(requests)-1]
}

func requireContains(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("missing %q in:\n%s", w, text)
		}
	}
}

func requireOmits(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Fatalf("unexpected %q in:\n%s", u, text)
		}
	}
}

func TestClaudeSessionCatchesUpHostRepliesAndAdvancesOnSuccess(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "Mira", "What was it?")
	requests := readClaudeRequests(t, c.log)
	if len(requests) != 2 {
		t.Fatalf("requests = %+v", requests)
	}
	second := requests[1]
	if second.Resume != "claude-fixture" {
		t.Fatalf("second request did not resume the session: %+v", second)
	}
	requireContains(t, second.Prompt, `"role":"user","content":"Mira","speaker":"User"`, `"content":"Mira here.","persona":"Mira"`, "User: What was it?")
	requireOmits(t, second.Prompt, "cobalt", "Mira reply 1.", "Recent conversation:")

	transcript := c.selector.Transcript()
	cursor := c.saved().PersonaContexts["mira"]
	if len(transcript) != 6 || cursor.Session == nil || cursor.Session.From != 0 || cursor.Through != len(transcript) || !cursor.Matches(transcript) {
		t.Fatalf("saved cursor = %+v for %d turns", cursor, len(transcript))
	}
}

func TestClaudeInterruptedReplyDoesNotAdvanceItsCursor(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	var once sync.Once
	events.Subscribe(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
	if err := c.p.RunTurnTextMode(turnCtx, "What was it? "+claudeStall); err == nil {
		t.Fatal("interrupted turn reported success")
	}
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Through != 2 {
		t.Fatalf("cursor after interruption = %+v", cursor)
	}

	c.say("Say it again.")
	last := lastClaudeRequest(t, c.log)
	if last.Resume != "claude-fixture" {
		t.Fatalf("request after interruption = %+v", last)
	}
	requireContains(t, last.Prompt, `"content":"What was it? `+claudeStall, "Incomplete assistant response", claudePartial, "User: Say it again.")
	requireOmits(t, last.Prompt, "cobalt")
	transcript := c.selector.Transcript()
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) || len(transcript) != 6 {
		t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
	}
}

func TestResumedClaudeConversationReplaysItsShareIntoFreshSession(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "The door is teal.")
	saved := c.saved()
	if cursor := saved.PersonaContexts["mira"]; cursor.Session == nil || cursor.Through != 4 {
		t.Fatalf("saved cursor = %+v", cursor)
	}

	c.start("mira", saved.Turns, saved.PersonaContexts)
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Through != 4 || !cursor.Matches(saved.Turns) {
		t.Fatalf("resumed cursor = %+v", cursor)
	}
	c.say("What do you remember?")
	last := lastClaudeRequest(t, c.log)
	if last.Resume != "" {
		t.Fatalf("a restarted provider resumed %q", last.Resume)
	}
	requireContains(t, last.Prompt, "cobalt", "The door is teal.", `"content":"Mira reply 1.","persona":"Mira"`, "Mira reply 2.", "User: What do you remember?")
	requireOmits(t, last.Prompt, "omitted]")
}

func TestClaudeShareStartsWhereItsPersonaWasFirstAddressed(t *testing.T) {
	c := newClaudeConversation(t, 20)
	earlier := []brain.Turn{
		{Role: "user", Content: "Private: the vault code is cobalt."},
		{Role: "assistant", Content: "Noted privately.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "earlier"},
	}
	if err := c.store.SaveSelectionState(c.sess, earlier, events.PersonaSelection{PersonaID: "mira", DisplayName: "Mira"}, brain.PersonaContexts{}); err != nil {
		t.Fatal(err)
	}
	saved := c.saved()
	c.start("mira", saved.Turns, saved.PersonaContexts)
	for _, turn := range c.p.Brain.History() {
		requireOmits(t, turn.Content, "cobalt", "privately")
	}
	c.say("Hello.", "What is the code?")
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "cobalt", "privately")
	}
	if cursor := c.saved().PersonaContexts["mira"]; cursor.Session == nil || cursor.Session.From != len(earlier) {
		t.Fatalf("cursor = %+v", cursor)
	}
	if turns := c.selector.Transcript(); len(turns) != len(earlier)+4 || turns[0].Content != earlier[0].Content {
		t.Fatalf("the earlier conversation must stay in the transcript: %+v", turns)
	}
}
