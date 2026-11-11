package personaruntime

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// promptRecord is one conversation record line in a Claude request.
type promptRecord struct{ Role, Content, Speaker, Persona string }

func promptRecords(t *testing.T, prompt string) []promptRecord {
	t.Helper()
	var out []promptRecord
	for line := range strings.Lines(prompt) {
		if !strings.HasPrefix(line, `{"role":`) {
			continue
		}
		var r promptRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func recordsOf(turns []brain.Turn) []promptRecord {
	out := make([]promptRecord, len(turns))
	for i, turn := range turns {
		out[i] = promptRecord{Role: turn.Role, Content: turn.Content, Persona: turn.PersonaDisplayName}
		if turn.Role == "user" {
			out[i].Speaker = "User"
		}
	}
	return out
}

type spoken struct{ role, content, persona string }

func requireTranscript(t *testing.T, turns []brain.Turn, want []spoken) {
	t.Helper()
	if len(turns) != len(want) {
		t.Fatalf("transcript has %d turns, want %d: %+v", len(turns), len(want), turns)
	}
	seen := map[string]bool{}
	for i, w := range want {
		got := turns[i]
		if got.Role != w.role || got.Content != w.content || got.PersonaID != w.persona {
			t.Fatalf("turn %d = %s/%q/%s, want %s/%q/%s", i, got.Role, got.Content, got.PersonaID, w.role, w.content, w.persona)
		}
		if got.Role == "assistant" {
			if got.ResponseID == "" || seen[got.ResponseID] {
				t.Fatalf("turn %d response id %q is missing or reused", i, got.ResponseID)
			}
			seen[got.ResponseID] = true
		}
	}
}

// requireExited waits for every fixture process to be reaped.
func requireExited(t *testing.T, requests []claudeRequest) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, req := range requests {
		for processRunning(req.PID) {
			if time.Now().After(deadline) {
				t.Fatalf("claude fixture process %d is still running", req.PID)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func processRunning(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestOllamaAndClaudePersonasTakeTurnsInOneConversation(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.say("Note that the vault code is cobalt.", "switch to Mira", "What do you think?")
	mira := c.p.Brain
	for _, turn := range mira.History() {
		requireOmits(t, turn.Content, "Note that the vault code")
	}
	c.say("switch to Ember", "What color is the door?", "switch to Mira")
	if c.p.Brain != mira {
		t.Fatal("returning to Mira did not reuse its parked provider")
	}
	c.say("Do you agree?")

	transcript := c.selector.Transcript()
	requireTranscript(t, transcript, []spoken{
		{"user", "Note that the vault code is cobalt.", ""}, {"assistant", emberReply, "ember"},
		{"user", "switch to Mira", ""}, {"assistant", "Mira here.", "mira"},
		{"user", "What do you think?", ""}, {"assistant", "Mira reply 1.", "mira"},
		{"user", "switch to Ember", ""}, {"assistant", "Ember here.", "ember"},
		{"user", "What color is the door?", ""}, {"assistant", emberReply, "ember"},
		{"user", "switch to Mira", ""}, {"assistant", "Mira here.", "mira"},
		{"user", "Do you agree?", ""}, {"assistant", "Mira reply 2.", "mira"},
	})

	requests := readClaudeRequests(t, c.log)
	if len(requests) != 2 {
		t.Fatalf("claude requests = %+v", requests)
	}
	first, second := requests[0], requests[1]
	if first.Resume != "" {
		t.Fatalf("first request resumed %q", first.Resume)
	}
	if second.Resume != first.Session {
		t.Fatalf("Mira's session %q did not survive Ember speaking: %+v", first.Session, second)
	}
	// Each request carries exactly the lines Mira's session had not seen,
	// starting where Mira was first addressed.
	if got, want := promptRecords(t, first.Prompt), recordsOf(transcript[2:4]); !slices.Equal(got, want) {
		t.Fatalf("first catch-up = %+v, want %+v", got, want)
	}
	if got, want := promptRecords(t, second.Prompt), recordsOf(transcript[6:12]); !slices.Equal(got, want) {
		t.Fatalf("second catch-up = %+v, want %+v", got, want)
	}
	requireContains(t, first.Prompt, "User: What do you think?")
	requireContains(t, second.Prompt, "User: Do you agree?")
	for _, req := range requests {
		requireOmits(t, req.Prompt, "You are Ember.", "Note that the vault code")
	}

	ollama := c.ollamaRequests()
	if len(ollama) != 2 {
		t.Fatalf("ollama requests = %d", len(ollama))
	}
	var returned strings.Builder
	for _, m := range ollama[1] {
		returned.WriteString(m.Role + ": " + m.Content + "\n")
		if m.Role == "assistant" && strings.Contains(m.Content, "Mira reply") {
			t.Fatalf("Ember was handed Mira's reply as its own:\n%s", returned.String())
		}
	}
	requireContains(t, returned.String(), "Mira reply 1.", "What do you think?")

	cursor := c.selector.ContextSnapshots()["mira"]
	if cursor.Session == nil || cursor.Session.From != 2 || cursor.Through != len(transcript) || !cursor.Matches(transcript) {
		t.Fatalf("Mira's cursor = %+v", cursor)
	}
	requireExited(t, requests)
}

func TestClaudeCatchUpTooLargeForOneRequestStartsFreshSession(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	// A 100-token session fuse bounds one catch-up to 200 bytes.
	c.base.ClaudeMaxSessionTokens = 100
	c.start("ember", nil, nil)
	c.say("switch to Mira", "What do you think?", "switch to Ember", "Note that the vault code is cobalt.", "What color is the door?", "switch to Mira", "Do you agree?")

	requests := readClaudeRequests(t, c.log)
	if len(requests) != 2 {
		t.Fatalf("claude requests = %+v", requests)
	}
	first, second := requests[0], requests[1]
	requireOmits(t, first.Prompt, "omitted]")
	if second.Resume != "" || second.Session == first.Session {
		t.Fatalf("an oversized catch-up resumed the old session: %+v", second)
	}
	transcript := c.selector.Transcript()
	records := promptRecords(t, second.Prompt)
	if len(records) == 0 || len(records) >= 12 {
		t.Fatalf("replay records = %+v", records)
	}
	if want := recordsOf(transcript[12-len(records) : 12]); !slices.Equal(records, want) {
		t.Fatalf("replay = %+v, want the newest lines %+v", records, want)
	}
	requireContains(t, second.Prompt, "earlier conversation records omitted]", "User: Do you agree?")
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
		t.Fatalf("cursor after the replay = %+v for %d turns", cursor, len(transcript))
	}
}

func TestInterruptedClaudeReplyIsCaughtUpAfterAnotherPersonaSpeaks(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.say("switch to Mira", "What do you think?")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	var once sync.Once
	events.Subscribe(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
	if err := c.p.RunTurnTextMode(turnCtx, "Say more. "+claudeStall); err == nil {
		t.Fatal("interrupted turn reported success")
	}
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != 4 {
		t.Fatalf("an interrupted reply advanced the cursor: %+v", cursor)
	}
	c.say("switch to Ember", "What color is the door?", "switch to Mira", "Go on.")

	transcript := c.selector.Transcript()
	if partial := transcript[5]; partial.PersonaID != "mira" || partial.Disposition != "interrupted" || partial.Content != claudePartial {
		t.Fatalf("interrupted reply = %+v", partial)
	}
	requests := readClaudeRequests(t, c.log)
	last := requests[len(requests)-1]
	if last.Resume != requests[0].Session {
		t.Fatalf("request after the interruption = %+v", last)
	}
	records := promptRecords(t, last.Prompt)
	want := recordsOf(transcript[4:12])
	want[1].Content = "[Incomplete assistant response; not a completed answer.]\n" + claudePartial
	if !slices.Equal(records, want) {
		t.Fatalf("catch-up = %+v, want %+v", records, want)
	}
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
		t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
	}
	requireExited(t, requests)
}

func TestResumedMixedConversationAddressesClaudeAgain(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.say("Note that the vault code is cobalt.", "switch to Mira", "What do you think?", "switch to Ember", "What color is the door?")
	saved := c.saved()
	mira, ember := saved.PersonaContexts["mira"], saved.PersonaContexts["ember"]
	if saved.SelectedPersonaID != "ember" || len(saved.Turns) != 10 {
		t.Fatalf("saved session = %+v", saved)
	}
	if mira.Session == nil || mira.Session.From != 2 || mira.Through != 6 || !mira.Matches(saved.Turns) {
		t.Fatalf("saved Mira cursor = %+v", mira)
	}
	if ember.Session != nil || ember.Through != 10 || !ember.Matches(saved.Turns) {
		t.Fatalf("saved Ember context = %+v", ember)
	}

	c.start(saved.SelectedPersonaID, saved.Turns, saved.PersonaContexts)
	if got := c.selector.Snapshot().PersonaID; got != "ember" {
		t.Fatalf("resumed selection = %q", got)
	}
	resumed := c.selector.Transcript()
	for i := range saved.Turns {
		if resumed[i].Content != saved.Turns[i].Content || resumed[i].PersonaID != saved.Turns[i].PersonaID || resumed[i].ResponseID != saved.Turns[i].ResponseID {
			t.Fatalf("resumed turn %d = %+v, saved %+v", i, resumed[i], saved.Turns[i])
		}
	}
	contexts := c.selector.ContextSnapshots()
	if got := contexts["mira"]; got.Through != mira.Through || got.PrefixHash != mira.PrefixHash || got.Session == nil || got.Session.From != mira.Session.From {
		t.Fatalf("resumed Mira cursor = %+v, saved %+v", got, mira)
	}
	if got := contexts["ember"]; got.Through != ember.Through || got.PrefixHash != ember.PrefixHash {
		t.Fatalf("resumed Ember context = %+v, saved %+v", got, ember)
	}

	c.say("switch to Mira", "Do you agree?")
	requests := readClaudeRequests(t, c.log)
	last := requests[len(requests)-1]
	if len(requests) != 2 || last.Resume != "" {
		t.Fatalf("a restarted provider must start a new session: %+v", requests)
	}
	transcript := c.selector.Transcript()
	if got, want := promptRecords(t, last.Prompt), recordsOf(transcript[2:12]); !slices.Equal(got, want) {
		t.Fatalf("replay after resume = %+v, want %+v", got, want)
	}
	if reply := transcript[len(transcript)-1]; reply.PersonaID != "mira" || reply.Content != "Mira reply 2." {
		t.Fatalf("reply after resume = %+v", reply)
	}
}

func TestEditedClaudePromptRetiresItsParkedSession(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.say("switch to Mira", "What do you think?", "switch to Ember")
	if len(c.selector.parked) != 1 {
		t.Fatalf("parked = %+v", c.selector.parked)
	}
	mira := c.selector.parked[0].provider
	writeTestPrompt(t, c.base.PromptsDir, "mira", "You are {agent_name}, and brief.")
	c.say("switch to Mira", "Still there?")
	if c.p.Brain == mira || len(c.selector.parked) != 0 {
		t.Fatalf("an edited persona reused its parked session; parked = %+v", c.selector.parked)
	}
	requests := readClaudeRequests(t, c.log)
	last := requests[len(requests)-1]
	if last.Resume != "" {
		t.Fatalf("an edited persona resumed %q", last.Resume)
	}
	requireContains(t, last.Prompt, `"content":"What do you think?"`, `"content":"Mira reply 1.","persona":"Mira"`)
}

func TestOllamaHandsTheFloorToClaudeByName(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.p.RouteHandoff = c.selector.RouteHandoff
	c.mu.Lock()
	c.emberSays = "@Mira what color do you think the door is?"
	c.mu.Unlock()
	c.say("Note that the door is teal.")

	transcript := c.selector.Transcript()
	requireTranscript(t, transcript, []spoken{
		{"user", "Note that the door is teal.", ""},
		{"assistant", "@Mira what color do you think the door is?", "ember"},
		{"assistant", "Mira reply 1.", "mira"},
	})
	request := lastClaudeRequest(t, c.log)
	if got, want := promptRecords(t, request.Prompt), recordsOf(transcript[1:2]); !slices.Equal(got, want) {
		t.Fatalf("handoff catch-up = %+v, want %+v", got, want)
	}
	requireContains(t, request.Prompt, "The latest agent is handing you the floor")
	requireOmits(t, request.Prompt, "door is teal", "\nUser:")
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Session.From != 1 || cursor.Through != 3 {
		t.Fatalf("Mira's cursor = %+v", cursor)
	}
}
