package personaruntime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestClearRetiresParkedClaudeSessionsImmediately(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "switch to Ember")
	if len(c.selector.parked) != 1 {
		t.Fatalf("parked = %+v", c.selector.parked)
	}
	if err := c.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if len(c.selector.parked) != 0 {
		t.Fatalf("a cleared conversation still holds a parked Claude session until the next selection: %+v", c.selector.parked)
	}
}

func TestLiveClaudeSessionThatLostItsCursorStillCatchesUpAfterASwitch(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.")
	session := lastClaudeRequest(t, c.log).Session
	c.selector.transcript.mu.Lock()
	delete(c.selector.transcript.sessions, "mira")
	c.selector.transcript.mu.Unlock()

	c.say("The door is teal.", "switch to Ember", "What color is the car?", "switch to Mira", "And you?")
	last := lastClaudeRequest(t, c.log)
	if last.Resume != session {
		t.Fatalf("Mira's session was not kept once her cursor was lost: resumed %q, want %q", last.Resume, session)
	}
	requireContains(t, last.Prompt, "What color is the car?", emberReply, "User: And you?")
	requireOmits(t, last.Prompt, "The door is teal.")
}

func TestClaudeSessionStartedFromACompactionSummaryResumesWithoutIt(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "The door is teal.")
	c.p.CompactPrompt = "Summarize the conversation."
	if err := c.p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.say("What do you remember?")
	seeded := lastClaudeRequest(t, c.log)
	if seeded.Resume != "" {
		t.Fatalf("the turn after compaction resumed %q", seeded.Resume)
	}
	requireContains(t, seeded.Prompt, claudeSummary)

	c.say("And the car?")
	resumed := lastClaudeRequest(t, c.log)
	if resumed.Resume != seeded.Session {
		t.Fatalf("the session started from the summary was not resumed: %+v", resumed)
	}
	requireContains(t, resumed.Prompt, "User: And the car?")
	requireOmits(t, resumed.Prompt, claudeSummary, "What do you remember?", "cobalt")
	transcript := c.selector.Transcript()
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
		t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
	}

	saved := c.saved()
	c.start("mira", saved.Turns, saved.PersonaContexts)
	c.say("Still there?")
	restarted := lastClaudeRequest(t, c.log)
	if restarted.Resume != "" {
		t.Fatalf("a restarted provider resumed %q", restarted.Resume)
	}
	requireContains(t, restarted.Prompt, "cobalt", "And the car?", "User: Still there?")
	requireOmits(t, restarted.Prompt, claudeSummary, "omitted]")
}

func TestLargeClaudeRequestKeepsEveryArgumentUnderTheArgvLimit(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newClaudeConversation(t, 200)
	line := strings.Repeat("cobalt ", 1500)
	for i := range 10 {
		c.say(fmt.Sprintf("Line %d: %s", i, line))
	}
	saved := c.saved()
	c.start("mira", saved.Turns, saved.PersonaContexts)
	paste := "PASTE-START " + strings.Repeat("teal ", 20000) + " PASTE-END"
	c.say(paste)
	last := lastClaudeRequest(t, c.log)
	if last.Resume != "" {
		t.Fatalf("a restarted provider resumed %q", last.Resume)
	}
	if len(last.Prompt) < 128<<10 {
		t.Fatalf("prompt is %d bytes; the test must exceed one argument's limit", len(last.Prompt))
	}
	const argMax = 128 << 10
	if last.MaxArg >= argMax {
		t.Fatalf("one argument was %d bytes, at or over the %d byte limit Linux allows", last.MaxArg, argMax)
	}
	requireContains(t, last.Prompt, "PASTE-START", "PASTE-END", "Line 9:", "omitted]")
}

// A prompt one byte under the inline limit travels as one argument, and one
// at the limit on stdin, on the streaming and the blocking path alike. Either
// way the whole prompt arrives and no argument reaches the 128 KiB Linux
// allows.
func TestClaudePromptsAtTheInlineLimitKeepEveryArgumentUnderTheArgvLimit(t *testing.T) {
	const inline, argMax = 100 << 10, 128 << 10
	c := newClaudeConversation(t, 20)
	b := c.p.Brain.(*brain.Brain)
	ask := func(stream bool, input string) claudeRequest {
		t.Helper()
		if stream {
			s, err := b.ThinkStream(t.Context(), input, brain.StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for range s.Chunks {
			}
			if result := <-s.Done; result.Err != nil {
				t.Fatal(result.Err)
			}
		} else if _, err := b.ThinkFull(t.Context(), input, brain.StreamOptions{}); err != nil {
			t.Fatal(err)
		}
		return lastClaudeRequest(t, c.log)
	}
	ask(true, "Hello.")
	for _, stream := range []bool{true, false} {
		for _, size := range []int{inline - 1, inline} {
			probe := ask(stream, "probe")
			if probe.Resume == "" {
				t.Fatalf("the probe started a new session: %+v", probe)
			}
			const start, end = "PASTE-START ", " PASTE-END"
			filler := size - (len(probe.Prompt) - len("probe")) - len(start) - len(end)
			got := ask(stream, start+strings.Repeat("t", filler)+end)
			if len(got.Prompt) != size || !strings.Contains(got.Prompt, start) || !strings.Contains(got.Prompt, end) {
				t.Fatalf("stream %v: a %d byte prompt arrived as %d bytes", stream, size, len(got.Prompt))
			}
			if got.MaxArg >= argMax {
				t.Fatalf("stream %v: one argument was %d bytes", stream, got.MaxArg)
			}
			if inArgv := got.MaxArg >= size; inArgv != (size < inline) {
				t.Fatalf("stream %v: a %d byte prompt went as an argument: %v", stream, size, inArgv)
			}
			if !stream && got.Format != "json" || stream && got.Format != "stream-json" {
				t.Fatalf("stream %v: request format %q", stream, got.Format)
			}
		}
	}
}

func TestAppendSeenLeavesATurnRecordedDuringTheRequestUnseen(t *testing.T) {
	tr := newTranscript(func() string { return "e" }, nil)
	if err := tr.Append("e", brain.Turn{Role: "user", Content: "one"}, brain.Turn{Role: "assistant", Content: "Mira one.", PersonaID: "mira", ResponseID: "m1"}); err != nil {
		t.Fatal(err)
	}
	cursor := brain.SessionContext(tr.Turns(), 0, 2)
	tr.setLive("mira", &cursor)
	_, covered, ok := tr.CatchUp("mira")
	if !ok || covered != 2 {
		t.Fatalf("catch-up covered %d, ok %v", covered, ok)
	}
	if err := tr.Append("e", brain.Turn{Role: "assistant", Content: "Ember's job is done.", PersonaID: "ember", ResponseID: "e1"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendSeen("e", "mira", covered, brain.Turn{Role: "user", Content: "two"}, brain.Turn{Role: "assistant", Content: "Mira two.", PersonaID: "mira", ResponseID: "m2"}); err != nil {
		t.Fatal(err)
	}
	next, _, ok := tr.CatchUp("mira")
	if !ok || next.Seen != 2 {
		t.Fatalf("catch-up = %+v, ok %v", next, ok)
	}
	var unseen []string
	for _, turn := range next.Turns[next.Seen:] {
		unseen = append(unseen, turn.Content)
	}
	if want := []string{"Ember's job is done.", "two", "Mira two."}; strings.Join(unseen, "|") != strings.Join(want, "|") {
		t.Fatalf("unseen = %q, want %q", unseen, want)
	}
}
