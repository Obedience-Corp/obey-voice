package personaruntime

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
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

func TestClaudeTurnAfterCompactionStartsFromTheSummary(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "The door is teal.", "Mira")
	c.p.CompactPrompt = "Summarize the conversation."
	if err := c.p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	requests := readClaudeRequests(t, c.log)
	summary := requests[len(requests)-1]
	if summary.Format != "json" || summary.Resume == "" {
		t.Fatalf("summary request = %+v", summary)
	}
	transcript := c.selector.Transcript()
	if got, want := promptRecords(t, summary.Prompt), recordsOf(transcript[4:6]); !slices.Equal(got, want) {
		t.Fatalf("the summary did not cover the unseen lines: %+v, want %+v", got, want)
	}

	c.say("Mira", "What do you remember?")
	requests = readClaudeRequests(t, c.log)
	if len(requests) != 4 {
		t.Fatalf("requests = %+v", requests)
	}
	last := requests[3]
	if last.Resume != "" {
		t.Fatalf("the turn after compaction resumed %q", last.Resume)
	}
	transcript = c.selector.Transcript()
	want := append([]promptRecord{{Role: "assistant", Content: claudeSummary}}, recordsOf(transcript[6:8])...)
	if got := promptRecords(t, last.Prompt); !slices.Equal(got, want) {
		t.Fatalf("after compaction = %+v, want the summary and the lines after it %+v", got, want)
	}
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
		t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
	}
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

func TestCatchUpSharesSpokenLinesOnly(t *testing.T) {
	turns := []brain.Turn{
		{Role: "user", Content: "Note the door is teal."},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
		{Role: "user", Content: "Read the vault file.", Attachments: []chatmedia.Ref{{ID: "a1", Name: "vault-scan.png"}}},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []brain.TurnToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"cat ~/private/vault.txt"}`}}, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
		{Role: "tool", ToolName: "bash", ToolCallID: "c1", Content: "secret tool output"},
		{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "c2", Name: "bash"}}, PersonaID: "ember", ResponseID: "e1"},
		{Role: "assistant", Content: "The vault code is cobalt.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	tr := newTranscript(func() string { return "epoch" }, turns)
	cursor := brain.SessionContext(turns, 0, 2)
	tr.setLive("mira", &cursor)
	catchUp, covered, ok := tr.CatchUp("mira")
	if !ok || covered != len(turns) || catchUp.Seen != 2 {
		t.Fatalf("catch-up = %+v, covered %d, ok %v", catchUp, covered, ok)
	}
	var lines []string
	for _, turn := range catchUp.Turns {
		if turn.Role == "tool" || len(turn.ToolCalls) > 0 || turn.ToolName != "" || turn.ToolCallID != "" || len(turn.Attachments) > 0 {
			t.Fatalf("catch-up carries tool traffic or attachments: %+v", turn)
		}
		lines = append(lines, turn.Content)
	}
	if want := []string{"Note the door is teal.", "Noted.", "Read the vault file.", "Let me look.", "The vault code is cobalt."}; !slices.Equal(lines, want) {
		t.Fatalf("shared lines = %q, want %q", lines, want)
	}
	if len(tr.Turns()[3].ToolCalls) != 1 {
		t.Fatal("sharing a line rewrote the recorded transcript")
	}
}

func TestClaudeNeverReceivesAnotherPersonasToolTraffic(t *testing.T) {
	c := newClaudeConversation(t, 20)
	turns := []brain.Turn{
		{Role: "user", Content: "Mira, hello."},
		{Role: "assistant", Content: "Hello.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
		{Role: "user", Content: "Ember, check my notes."},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []brain.TurnToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"cat ~/private/diary.md"}`}}, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
		{Role: "tool", ToolName: "bash", ToolCallID: "c1", Content: "TOOL OUTPUT SECRET"},
		{Role: "assistant", Content: "Your notes say hi.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	c.start("mira", turns, brain.PersonaContexts{"mira": brain.SessionContext(turns, 0, 2)})
	for _, turn := range c.p.Brain.History() {
		if len(turn.ToolCalls) > 0 || turn.ToolName != "" || turn.Role == "tool" {
			t.Fatalf("the provider holds another persona's tool traffic: %+v", turn)
		}
	}
	c.say("What did Ember find?")
	last := lastClaudeRequest(t, c.log)
	requireContains(t, last.Prompt, `"content":"Let me look.","persona":"Ember"`, "Your notes say hi.")
	requireOmits(t, last.Prompt, "diary.md", "TOOL OUTPUT SECRET", "tool_call", `"bash"`)
}

func TestSessionShareStartIsTheTurnThatFirstAddressedThePersona(t *testing.T) {
	asked := []brain.Turn{
		{Role: "user", Content: "Note the door is teal."},
		{Role: "assistant", Content: "Noted.", PersonaID: "ember"},
		{Role: "user", Content: "Mira, what do you think?"},
		{Role: "assistant", Content: "Teal is fine.", PersonaID: "mira"},
	}
	handedOff := []brain.Turn{
		{Role: "user", Content: "Note the door is teal."},
		{Role: "assistant", Content: "@Mira what do you think?", PersonaID: "ember"},
		{Role: "assistant", Content: "Teal is fine.", PersonaID: "mira"},
	}
	for _, tc := range []struct {
		name    string
		turns   []brain.Turn
		handoff bool
		want    int
	}{
		{"asked by the user", asked, false, 2},
		{"handed the floor", handedOff, false, 1},
		{"not addressed yet", asked[:2], false, 2},
		{"being handed the floor", handedOff[:2], true, 1},
	} {
		if got := sessionShareStart(tc.turns, "mira", tc.handoff); got != tc.want {
			t.Errorf("%s: share start = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestClaudeSelectedAtLaunchButNeverAddressedStartsWhereItIsAddressed(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("switch to Ember", "Note that the vault code is cobalt.", "switch to Mira", "What do you think?")
	requests := readClaudeRequests(t, c.log)
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	transcript := c.selector.Transcript()
	if got, want := promptRecords(t, requests[0].Prompt), recordsOf(transcript[4:6]); !slices.Equal(got, want) {
		t.Fatalf("catch-up = %+v, want %+v", got, want)
	}
	requireOmits(t, requests[0].Prompt, "Note that the vault code", "switch to Ember")
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Session.From != 4 {
		t.Fatalf("cursor = %+v", cursor)
	}
}
