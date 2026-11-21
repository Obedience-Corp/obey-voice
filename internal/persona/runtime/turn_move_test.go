package personaruntime

import (
	"context"
	"os"
	"path/filepath"
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

// holdAndMove starts Anika on a line that reads the notes and is then held,
// and moves her turn once its first text has arrived.
func (c *movingLocalConversation) holdAndMove(next string) {
	c.t.Helper()
	streaming := make(chan struct{})
	var once sync.Once
	stop := events.SubscribeDetachable(c.p.Events, func(e events.ResponseDelta) {
		if strings.Contains(e.Text, "Let me think") {
			once.Do(func() { close(streaming) })
		}
	})
	defer stop()
	turn := make(chan error, 1)
	go func() {
		turn <- c.p.RunTurnTextMode(c.t.Context(), "Read the notes, then "+ollamaStall+" "+ollamaRelease)
	}()
	select {
	case <-streaming:
	case <-time.After(10 * time.Second):
		c.t.Fatal("Anika's reply never started")
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

// Obey answers while Anika's moved request is still running. Obey's provider
// is given the conversation as it stood when Anika's request started, her
// user line and her partial reply, and none of the tool round she ran since.
// Anika's provider stays open until her request ends; addressing her again
// meanwhile gives her a fresh provider. Her finished reply is recorded once.
func TestMovedTurnRunsOnWhileAnotherPersonaAnswers(t *testing.T) {
	c := newMovingLocalConversation(t)
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
	obeyRequest := string(sent[len(sent)-1])
	if !strings.Contains(obeyRequest, "what is the vault code?") || !strings.Contains(obeyRequest, ollamaRelease) || !strings.Contains(obeyRequest, "Let me think about") {
		t.Fatalf("Obey was not sent Anika's line and partial reply:\n%s", obeyRequest)
	}
	if strings.Contains(obeyRequest, "notes.txt") {
		t.Fatalf("Obey was sent the tool round of Anika's running request:\n%s", obeyRequest)
	}
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
		if finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || !strings.Contains(finished.Summary, ollamaReleased) {
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

// Only a line naming another known persona by the exact rules moves a turn.
func TestNamesOtherPersonaUsesOnlyTheExactRules(t *testing.T) {
	c := newMovingLocalConversation(t)
	c.say("switch to Anika")
	for input, want := range map[string]bool{
		"Hey Obey, what is the vault code?": true,
		"@obey what is the vault code?":     true,
		"Obey, help me with this.":          true,
		"Anika, stop that.":                 false,
		"Hey Zed, are you there?":           false,
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
