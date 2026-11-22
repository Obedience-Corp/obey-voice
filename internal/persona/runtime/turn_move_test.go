package personaruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// closeWatch is an Ollama persona provider that reports when the host closes it.
type closeWatch struct {
	*brain.OllamaBrain
	closed chan struct{}
	once   sync.Once
}

func (w *closeWatch) Close() error {
	w.once.Do(func() { close(w.closed) })
	return w.OllamaBrain.Close()
}

func (w *closeWatch) isClosed() bool {
	select {
	case <-w.closed:
		return true
	default:
		return false
	}
}

// movingLocalConversation is a local conversation between Obey and Anika
// with background jobs, where every provider a switch creates is watched.
type movingLocalConversation struct {
	*localConversation
	mu        sync.Mutex
	providers []*closeWatch
	finished  chan events.JobFinished
}

func newMovingLocalConversation(t *testing.T) *movingLocalConversation {
	t.Helper()
	c := &movingLocalConversation{localConversation: newLocalConversationWithTools(t, 20, "", "obey", "anika"), finished: make(chan events.JobFinished, 4)}
	if err := os.WriteFile(filepath.Join(c.root, "notes.txt"), []byte("The vault code is cobalt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(c.p, jobs, nil, c.base, nil).Wire()
	c.p.NamesOtherPersona = c.selector.NamesOtherPersona
	events.Subscribe(c.p.Events, func(e events.JobFinished) { c.finished <- e })
	c.wrap = func(p brain.Provider) brain.Provider {
		ollama, ok := p.(*brain.OllamaBrain)
		if !ok {
			return p
		}
		w := &closeWatch{OllamaBrain: ollama, closed: make(chan struct{})}
		c.mu.Lock()
		c.providers = append(c.providers, w)
		c.mu.Unlock()
		return w
	}
	return c
}

func (c *movingLocalConversation) live() *closeWatch {
	c.t.Helper()
	w, ok := c.p.Brain.(*closeWatch)
	if !ok {
		c.t.Fatalf("live provider %T is not one a switch created", c.p.Brain)
	}
	return w
}

// anikaHeldLine reads the notes and is then held by the fake server.
const anikaHeldLine = "Read the notes, then " + ollamaStall + " " + ollamaRelease

// holdAndMove starts Anika on a line that reads the notes and is then held,
// and moves her turn once she has shown all the text sent before the hold.
func (c *movingLocalConversation) holdAndMove(next string) {
	c.t.Helper()
	c.holdLineAndMove(anikaHeldLine, next)
}

func (c *movingLocalConversation) holdLineAndMove(line, next string) {
	c.t.Helper()
	want := strings.TrimSpace(c.releasePartial(line))
	streaming := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var shown strings.Builder
	stop := events.SubscribeDetachable(c.p.Events, func(e events.ResponseDelta) {
		mu.Lock()
		shown.WriteString(e.Text)
		all := strings.TrimSpace(shown.String()) == want
		mu.Unlock()
		if all {
			once.Do(func() { close(streaming) })
		}
	})
	defer stop()
	turn := make(chan error, 1)
	go func() {
		turn <- c.p.RunTurnTextMode(c.t.Context(), line)
	}()
	select {
	case <-streaming:
	case err := <-turn:
		c.t.Fatalf("Anika's turn ended before it was moved: %v", err)
	case <-time.After(10 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		c.t.Fatalf("Anika showed %q, never %q", shown.String(), want)
	}
	if !c.p.MoveTurnToBackground(next) {
		c.t.Fatal("the running turn was not moved to the background")
	}
	select {
	case err := <-turn:
		if err != nil {
			c.t.Fatalf("moved turn returned %v", err)
		}
	case <-time.After(10 * time.Second):
		c.t.Fatal("moved turn did not return")
	}
}

// chatMessage is one message of a request Ollama was sent.
type chatMessage struct{ Role, Content string }

// conversationMessages are the messages of body after its system message.
func conversationMessages(t *testing.T, body []byte) []chatMessage {
	t.Helper()
	var req struct{ Messages []chatMessage }
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) == 0 || req.Messages[0].Role != "system" {
		t.Fatalf("request does not start with its system message: %s", body)
	}
	return req.Messages[1:]
}

// requireMovedRequestSent checks the request Obey answered with, message by
// message: Obey's earlier exchange, then one block holding the turns of the
// others in order and ending with Anika's held line and what stands for her
// reply, then Obey's own line, last.
func requireMovedRequestSent(t *testing.T, body []byte, line, reply string) {
	t.Helper()
	messages := conversationMessages(t, body)
	fail := func(why string) {
		t.Helper()
		var b strings.Builder
		for i, m := range messages {
			fmt.Fprintf(&b, "%d %s: %q\n", i, m.Role, m.Content)
		}
		t.Fatalf("%s; Obey was sent:\n%s", why, b.String())
	}
	if len(messages) != 4 {
		fail("want 4 messages")
	}
	if messages[0] != (chatMessage{"user", "User:\nNote that the vault code is cobalt."}) || messages[1] != (chatMessage{"assistant", "Reply 1."}) {
		fail("Obey's own exchange does not come first")
	}
	block := "Earlier in this conversation (context, not requests for you):\n\n" +
		"User:\nswitch to Anika\n\nAnika agent:\nAnika here.\n\n" +
		"User:\n" + line + "\n\nAnika agent:\n" + reply
	if messages[2] != (chatMessage{"user", block}) {
		fail("the others' turns are not one block ending with Anika's line and " + strconv.Quote(reply))
	}
	// The host's own context follows the line in the same message.
	if messages[3].Role != "user" || lastUserLine([]struct{ Role, Content string }{messages[3]}) != "Hey Obey, what is the vault code?" {
		fail("Obey's line is not the last message")
	}
	if body := string(body); strings.Contains(body, "notes.txt") || strings.Contains(body, "Let me think") {
		fail("Obey was sent Anika's tool round or the unfinished end of her reply")
	}
}

// Obey answers while Anika's moved request is still running. Obey's provider
// is given the conversation as it stood when Anika's request started, her
// user line and her partial reply, and none of the tool round she ran since.
// Anika's provider stays open until her request ends; addressing her again
// meanwhile gives her a fresh provider. Her finished reply is recorded once.
func TestMovedTurnRunsOnWhileAnotherPersonaAnswers(t *testing.T) {
	for _, byteAtATime := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "byte at a time"}[byteAtATime], func(t *testing.T) {
			movedTurnRunsOn(t, byteAtATime)
		})
	}
}

func movedTurnRunsOn(t *testing.T, byteAtATime bool) {
	c := newMovingLocalConversation(t)
	c.byteAtATime = byteAtATime
	c.say("Note that the vault code is cobalt.", "switch to Anika")
	busy := c.live()
	c.holdAndMove("Hey Obey, what is the vault code?")
	c.say("Hey Obey, what is the vault code?")

	if c.p.Brain == brain.Provider(busy) || c.selector.Snapshot().PersonaID != "obey" {
		t.Fatalf("Obey did not take the conversation: %+v", c.selector.Snapshot())
	}
	select {
	case <-c.heldStopped:
		t.Fatal("Anika's request was stopped")
	default:
	}
	if busy.isClosed() {
		t.Fatal("Anika's provider was closed while her request was running")
	}
	sent := c.requests()
	requireMovedRequestSent(t, sent[len(sent)-1], anikaHeldLine, "I read the notes.")
	if !strings.Contains(string(sent[len(sent)-2]), `"role":"tool"`) {
		t.Fatal("Anika's moved request did not run its tool round before it was held")
	}

	c.say("switch to Anika")
	if again := c.live(); again == busy {
		t.Fatal("addressing Anika again reused the provider her moved request runs on")
	}
	if busy.isClosed() {
		t.Fatal("Anika's provider was closed while her request was running")
	}

	close(c.release)
	select {
	case finished := <-c.finished:
		if finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || finished.Summary != " Let me think "+ollamaReleased {
			t.Fatalf("job finished = %+v", finished)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Anika's moved request never finished")
	}
	select {
	case <-busy.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Anika's old provider was not closed after her request ended")
	}
	c.say("Go on.")
	for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
		replies := countTurns(turns, func(t brain.Turn) bool {
			return t.Role == "assistant" && strings.Contains(t.Content, ollamaReleased)
		})
		lines := countTurns(turns, func(t brain.Turn) bool { return t.Role == "user" && strings.Contains(t.Content, ollamaRelease) })
		if replies != 1 || lines != 1 {
			t.Fatalf("%s holds %d replies and %d lines of the moved request: %+v", name, replies, lines, turns)
		}
		for _, turn := range turns {
			if turn.Role == "assistant" && strings.Contains(turn.Content, ollamaReleased) && turn.PersonaID != "anika" {
				t.Fatalf("%s attributes Anika's reply to %q", name, turn.PersonaID)
			}
		}
	}
}

// A request moved before its reply finished a sentence keeps no part of it.
// The persona answering next is still told the request was handed off, in the
// words a spawned request is recorded with, so it does not read as a line
// nobody answered. The job's reply is the whole reply.
func TestRequestMovedBeforeAnySentenceReadsAsHandedOff(t *testing.T) {
	for _, byteAtATime := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "byte at a time"}[byteAtATime], func(t *testing.T) {
			c := newMovingLocalConversation(t)
			c.byteAtATime = byteAtATime
			c.say("Note that the vault code is cobalt.", "switch to Anika")
			line := anikaHeldLine + " " + ollamaUnfinished
			c.holdLineAndMove(line, "Hey Obey, what is the vault code?")
			c.say("Hey Obey, what is the vault code?")
			sent := c.requests()
			requireMovedRequestSent(t, sent[len(sent)-1], line, "I'll work on that and report back.")

			close(c.release)
			select {
			case finished := <-c.finished:
				if finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || finished.Summary != "Let me think "+ollamaReleased {
					t.Fatalf("job finished = %+v", finished)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Anika's moved request never finished")
			}
		})
	}
}

// Only a line naming another known persona by the exact rules moves a turn.
func TestNamesOtherPersonaUsesOnlyTheExactRules(t *testing.T) {
	c := newMovingLocalConversation(t)
	c.say("switch to Anika")
	broken := filepath.Join(c.catalog.Dir, "zed")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "persona.yaml"), []byte("schema: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]bool{
		"Hey Obey, what is the vault code?": true,
		"@obey what is the vault code?":     true,
		"Obey, help me with this.":          true,
		"Anika, stop that.":                 false,
		"Hey Zed, are you there?":           false,
		"@zed what is the code?":            false,
		"Obey":                              true,
		"@obey":                             true,
		"What is the vault code?":           false,
		"stop":                              false,
	} {
		if got := c.selector.NamesOtherPersona(input); got != want {
			t.Errorf("NamesOtherPersona(%q) = %v, want %v", input, got, want)
		}
	}
}

// A persona whose provider keeps its own session is stopped as before when
// another persona is addressed; its request is not moved.
func TestTurnOnAProviderKeepingItsOwnSessionIsNotMoved(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "mira=codex")
	// Without tools the fixture CLI answers without a route request first.
	c.p.VoiceToolsEnabled = false
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(c.p, jobs, nil, c.base, nil).Wire()
	c.p.NamesOtherPersona = c.selector.NamesOtherPersona
	c.say("Note that the vault code is cobalt.", "switch to Mira")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	streaming := make(chan struct{})
	var once sync.Once
	stop := events.SubscribeDetachable(c.p.Events, func(events.ResponseDelta) { once.Do(func() { close(streaming) }) })
	defer stop()
	turn := make(chan error, 1)
	go func() { turn <- c.p.RunTurnTextMode(ctx, "Say more. "+claudeStall) }()
	select {
	case <-streaming:
	case <-time.After(10 * time.Second):
		t.Fatal("Mira's reply never started")
	}
	if !c.selector.NamesOtherPersona("Hey Ember, what is the vault code?") {
		t.Fatal("the line does not name Ember")
	}
	if c.p.MoveTurnToBackground("Hey Ember, what is the vault code?") {
		t.Fatal("a request on a provider keeping its own session was moved")
	}
	cancel()
	if err := <-turn; err == nil {
		t.Fatal("stopped turn reported success")
	}
}

// A moved reply whose job is stopped is saved as a stopped live reply is:
// its kept part and the text after it, marked unfinished, which later models
// are told.
func TestStoppedMovedReplyIsSavedUnfinished(t *testing.T) {
	c := newMovingLocalConversation(t)
	c.say("Note that the vault code is cobalt.", "switch to Anika")
	aborted := make(chan events.JobAborted, 1)
	events.Subscribe(c.p.Events, func(e events.JobAborted) { aborted <- e })
	c.holdAndMove("Hey Obey, what is the vault code?")
	c.say("Hey Obey, what is the vault code?")
	if err := c.p.CancelWork("job-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(10 * time.Second):
		t.Fatal("the job was not stopped")
	}
	for deadline := time.Now().Add(10 * time.Second); !c.p.BackgroundPending(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stopped reply was never recorded")
		}
	}
	c.say("Obey, go on.")
	var anika []brain.Turn
	for _, turn := range c.saved().Turns {
		if turn.Role == "assistant" && turn.PersonaID == "anika" && strings.Contains(turn.Content, "notes") || strings.Contains(turn.Content, "Let me think") {
			anika = append(anika, turn)
		}
	}
	if len(anika) != 2 || anika[0].Content != "I read the notes." || anika[1].Content != "Let me think" ||
		anika[0].Disposition != "interrupted" || anika[1].Disposition != "interrupted" {
		t.Fatalf("saved moved reply = %+v", anika)
	}
	sent := c.requests()
	if last := string(sent[len(sent)-1]); !strings.Contains(last, "Incomplete assistant response") {
		t.Fatalf("Obey was not told the reply was unfinished:\n%s", last)
	}
}
