package personaruntime

import (
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestStoppedCodexReplyIsRecordedOnceAsInterruptedAndReachesTheNextPersona(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "mira=codex")
	c.say("Note that the vault code is cobalt.", "switch to Mira")
	c.stopAfterFirstText("Say more. " + claudeStall)
	c.say("switch to Ember", "And now?")

	for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
		asked := func(t brain.Turn) bool { return t.Role == "user" && strings.Contains(t.Content, claudeStall) }
		if n := countTurns(turns, asked); n != 1 {
			t.Fatalf("%s holds the stopped line %d times: %+v", name, n, turns)
		}
		stopped := func(t brain.Turn) bool {
			return t.Role == "assistant" && t.PersonaID == "mira" && t.Disposition == "interrupted" && t.Content == claudePartial
		}
		if n := countTurns(turns, stopped); n != 1 {
			t.Fatalf("%s holds Mira's stopped reply %d times: %+v", name, n, turns)
		}
	}
	sent := c.lastOllamaRequest()
	requireContains(t, sent, "Say more. "+claudeStall, `Mira agent:\n[Incomplete assistant response; not a completed answer.]\n`+claudePartial)
	if strings.Count(sent, claudeStall) != 1 {
		t.Fatalf("Ember was sent the stopped line more than once:\n%s", sent)
	}

	// Mira's next request starts a new thread and replays the stopped exchange.
	c.say("switch to Mira", "Go on.")
	turns := readCodexTurns(t, c.codexLog)
	last := turns[len(turns)-1].Prompt
	requireContains(t, last, "Say more. "+claudeStall, claudePartial)
	if strings.Count(last, claudeStall) != 1 {
		t.Fatalf("Mira was sent the stopped line more than once:\n%s", last)
	}
}

func TestStoppedMuseSparkOrObeyRequestKeepsTheLineAndWhatWasRead(t *testing.T) {
	for _, provider := range []string{"muse-spark", "obey"} {
		t.Run(provider, func(t *testing.T) {
			c := newLocalConversation(t, 20, "ember", "mira="+provider)
			c.say("Note that the vault code is cobalt.", "switch to Mira")
			partial := ""
			if provider == "muse-spark" {
				// Muse Spark streams the start of its reply; Obey sends none before it ends.
				c.stopAfterFirstText("Say more. " + claudeStall)
				partial = claudePartial
			} else {
				c.stopOnceSent("Say more. "+claudeStall, func() bool {
					data, _ := os.ReadFile(c.mixedLog)
					return strings.Contains(string(data), claudeStall)
				})
			}
			c.say("switch to Ember", "And now?")

			for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
				asked := func(t brain.Turn) bool { return t.Role == "user" && strings.Contains(t.Content, claudeStall) }
				if n := countTurns(turns, asked); n != 1 {
					t.Fatalf("%s holds the stopped line %d times: %+v", name, n, turns)
				}
				stopped := func(t brain.Turn) bool { return t.Role == "assistant" && t.PersonaID == "mira" && t.Disposition != "" }
				want := 0
				if partial != "" {
					want = 1
				}
				if n := countTurns(turns, stopped); n != want {
					t.Fatalf("%s holds %d stopped replies from Mira, want %d: %+v", name, n, want, turns)
				}
			}
			requireContains(t, c.lastOllamaRequest(), "Say more. "+claudeStall, partial)
		})
	}
}

func TestStoppedOllamaReplyIsRecordedOnceAsTheTurnReadIt(t *testing.T) {
	c := newLocalConversation(t, 20, "ember")
	c.say("Note that the vault code is cobalt.")
	read := strings.TrimSpace(c.stopAfterFirstText(ollamaStall + " LONG, what else?"))
	if read == "" {
		t.Fatal("the turn read nothing")
	}
	// The stopped reply is recorded by the time the turn returns.
	check := func(name string, turns []brain.Turn) {
		t.Helper()
		asked := func(t brain.Turn) bool { return t.Role == "user" && strings.Contains(t.Content, ollamaStall) }
		if n := countTurns(turns, asked); n != 1 {
			t.Fatalf("%s holds the stopped line %d times: %+v", name, n, turns)
		}
		stopped := func(t brain.Turn) bool { return t.Role == "assistant" && t.Disposition != "" }
		if n := countTurns(turns, stopped); n != 1 {
			t.Fatalf("%s holds %d stopped replies: %+v", name, n, turns)
		}
		last := turns[len(turns)-1]
		if last.Role != "assistant" || last.PersonaID != "ember" || last.Disposition != "interrupted" || last.Content != read {
			t.Fatalf("%s recorded the stopped reply as %+v, but the turn read %q", name, last, read)
		}
	}
	check("transcript when the turn returned", c.selector.Transcript())
	c.say("And now?")
	turns := c.selector.Transcript()
	check("transcript", turns[:len(turns)-4])

	sent := c.lastOllamaRequest()
	requireContains(t, sent, `[Incomplete assistant response; not a completed answer.]\n`+read)
	if strings.Count(sent, "Let me think about") != 1 || strings.Count(sent, ollamaStall) != 1 || strings.Contains(sent, "w7") {
		t.Fatalf("Ember was sent the stopped exchange other than once as read:\n%s", sent)
	}
}
