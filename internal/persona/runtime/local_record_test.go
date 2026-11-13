package personaruntime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// stopAfterFirstText runs input as a turn and stops it once its first text
// is read. It returns the text the turn read.
func (c *localConversation) stopAfterFirstText(input string) string {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	var once sync.Once
	var mu sync.Mutex
	var read strings.Builder
	unsubscribe := events.SubscribeDetachable(c.p.Events, func(e events.ResponseDelta) {
		mu.Lock()
		read.WriteString(e.Text)
		mu.Unlock()
		once.Do(interrupt)
	})
	defer unsubscribe()
	if err := c.p.RunTurnTextMode(turnCtx, input); err == nil {
		c.t.Fatal("stopped turn reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	return read.String()
}

// stopOnceSent runs input as a turn and stops it once sent reports that its
// request reached the provider.
func (c *localConversation) stopOnceSent(input string, sent func() bool) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	go func() {
		for !sent() && turnCtx.Err() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		interrupt()
	}()
	if err := c.p.RunTurnTextMode(turnCtx, input); err == nil {
		c.t.Fatal("stopped turn reported success")
	}
}

// lastOllamaRequest renders the last request Ollama was sent.
func (c *localConversation) lastOllamaRequest() string {
	c.t.Helper()
	bodies := c.requests()
	if len(bodies) == 0 {
		c.t.Fatal("Ollama was sent nothing")
	}
	return requestLog(c.t, bodies[len(bodies)-1:])
}

func (c *localConversation) sayWith(refs []chatmedia.Ref, input string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	if err := c.p.RunTurnTextMode(chatmedia.WithInput(ctx, refs), input); err != nil {
		c.t.Fatalf("%q: %v", input, err)
	}
}

func countTurns(turns []brain.Turn, match func(brain.Turn) bool) int {
	n := 0
	for _, turn := range turns {
		if match(turn) {
			n++
		}
	}
	return n
}

// saidLine is a line of the conversation as its speaker said it.
type saidLine struct{ Speaker, Content string }

// spokenLines are the user's lines and the personas' replies, without the
// tool traffic between them.
func spokenLines(turns []brain.Turn) []saidLine {
	var out []saidLine
	for _, t := range turns {
		switch {
		case t.Role == "user":
			out = append(out, saidLine{"user", t.Content})
		case t.Role == "assistant" && strings.TrimSpace(t.Content) != "":
			out = append(out, saidLine{t.PersonaID, t.Content})
		}
	}
	return out
}

func TestEveryLineIsRecordedOnceInOrderWithItsSpeakerAcrossBackends(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "scout", "mira=codex")
	c.say("Note that the vault code is cobalt.", "switch to Scout", "The door is teal.", "switch to Mira", "What do you know?", "switch to Ember", "And now?")
	want := []saidLine{
		{"user", "Note that the vault code is cobalt."}, {"ember", "Reply 2."},
		{"user", "switch to Scout"}, {"scout", "Scout here."},
		{"user", "The door is teal."}, {"scout", "Reply 4."},
		{"user", "switch to Mira"}, {"mira", "Mira here."},
		{"user", "What do you know?"}, {"mira", "I remember cobalt."},
		{"user", "switch to Ember"}, {"ember", "Ember here."},
		{"user", "And now?"}, {"ember", "Reply 6."},
	}
	transcript := c.selector.Transcript()
	for name, turns := range map[string][]brain.Turn{"transcript": transcript, "saved": c.saved().Turns} {
		if got := spokenLines(turns); !slices.Equal(got, want) {
			t.Fatalf("%s = %+v\nwant %+v", name, got, want)
		}
	}
	for _, turn := range transcript {
		if turn.Role == "assistant" && (turn.ResponseID == "" || turn.PromptHash == "") {
			t.Fatalf("reply without its response identity: %+v", turn)
		}
	}
	sent := c.lastOllamaRequest()
	for _, line := range want[:len(want)-1] {
		if strings.Count(sent, line.Content) != 1 {
			t.Fatalf("Ember was sent %q %d times:\n%s", line.Content, strings.Count(sent, line.Content), sent)
		}
	}

	contexts := c.selector.ContextSnapshots()
	c.resume()
	if got := c.selector.Transcript(); !reflect.DeepEqual(spokenLines(got), want) || len(got) != len(transcript) {
		t.Fatalf("resumed transcript = %+v", got)
	}
	if got := c.selector.ContextSnapshots(); got["ember"].PrefixHash != contexts["ember"].PrefixHash || got["scout"].PrefixHash != contexts["scout"].PrefixHash || got["mira"].Session == nil {
		t.Fatalf("resumed views = %+v, want %+v", got, contexts)
	}
}

func TestCompactedLocalConversationContinuesWithEveryLineKept(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "scout")
	c.say("Note that the vault code is cobalt.", "And the door is teal.", "switch to Scout", "The key is under the mat.")
	c.p.CompactPrompt = "Summarize the conversation."
	if err := c.p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.say("What do you remember?", "switch to Ember", "And you?")
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	lines := spokenLines(c.saved().Turns)
	for _, want := range []string{"Note that the vault code is cobalt.", "And the door is teal.", "The key is under the mat.", "What do you remember?", "And you?"} {
		if !slices.Contains(lines, saidLine{"user", want}) {
			t.Fatalf("saved conversation lost %q: %+v", want, lines)
		}
	}
	assertRequestGolden(t, "compacted_local_conversation_requests.golden", c.requests())
}

func TestImageReachesTheOllamaPersonaItWasNotSaidTo(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "scout")
	ref, err := chatmedia.DefaultStore().Import("diagram.png", append(slices.Clone(testmedia.PNG()), 7))
	if err != nil {
		t.Fatal(err)
	}
	c.sayWith([]chatmedia.Ref{ref}, "What is in this diagram?")
	c.say("switch to Scout", "What did I show?")
	if turns := c.selector.Transcript(); !slices.Equal(turns[0].Attachments, []chatmedia.Ref{ref}) {
		t.Fatalf("recorded line = %+v", turns[0])
	}
	if sent := c.lastOllamaRequest(); !strings.Contains(sent, `"images":[`) {
		t.Fatalf("Scout was not sent the image:\n%s", sent)
	}
}

func TestLocalConversationWithAToolRoundSendsOllamaWhatItWasSentBefore(t *testing.T) {
	c := newLocalConversation(t, 6, "ember", "scout")
	c.p.RouterToolsPolicy = brain.RouterToolsLegacy
	if err := os.WriteFile(filepath.Join(c.root, "notes.txt"), []byte("The vault code is cobalt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.say("Read the notes, please.", "switch to Scout", "What did the notes say?", "switch to Ember", "Read the notes again.", "Thanks.")
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	turns := c.selector.Transcript()
	read := func(t brain.Turn) bool {
		return t.Role == "tool" && t.ToolName == "read_file" && strings.Contains(t.Content, "cobalt")
	}
	if n := countTurns(turns, read); n != 2 {
		t.Fatalf("recorded %d read_file results: %+v", n, turns)
	}
	assertRequestGolden(t, "tool_local_conversation_requests.golden", c.requests())
}

func TestJobResultForAnOllamaPersonaIsDeliveredAndRecordedOnce(t *testing.T) {
	c := newConversation(t, 20, "ember", "codex", "")
	c.work = true
	c.start("ember", nil, nil)
	c.say("Note that the vault code is cobalt.")
	job := c.startJob("ember-job", "ember", "Check the build")
	c.finishJob(job, "The build is green.")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := c.p.RunBackgroundTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if c.resultWaiting(job) {
		t.Fatal("the result is still waiting after an idle delivery")
	}
	c.say("Any news?")
	delivered := func(t brain.Turn) bool {
		return t.Role == "assistant" && t.PersonaID == "ember" && strings.Contains(t.Content, "The build is green.")
	}
	for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
		if n := countTurns(turns, delivered); n != 1 {
			t.Fatalf("%s holds the delivered result %d times: %+v", name, n, turns)
		}
	}
	requests := c.ollamaRequests()
	var sent strings.Builder
	for _, m := range requests[len(requests)-1] {
		sent.WriteString(m.Content + "\n")
	}
	if strings.Count(sent.String(), "The build is green.") != 1 {
		t.Fatalf("Ember was sent the result other than once:\n%s", sent.String())
	}
}
