package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// workingPersona keeps the shared history. A line with "HOLD" in it gets a
// first sentence, then waits for release before it finishes with last; any
// other line is answered with reply.
type workingPersona struct {
	reply, last string
	release     chan struct{}
	held        chan struct{}

	mu      sync.Mutex
	turns   []brain.Turn
	holdCtx context.Context
}

func newWorkingPersona(reply string) *workingPersona {
	return &workingPersona{reply: reply, last: "Found three files.", release: make(chan struct{}), held: make(chan struct{})}
}

func (b *workingPersona) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string), make(chan brain.StreamResult, 1)
	send := func(text string) bool {
		select {
		case out <- text:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		defer close(done)
		defer close(out)
		reply := b.reply
		if strings.Contains(input, "HOLD") {
			if !send("Working on it. ") {
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			}
			b.mu.Lock()
			b.holdCtx = ctx
			b.mu.Unlock()
			close(b.held)
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case <-b.release:
			}
			reply = b.last
		}
		if !send(reply) {
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		b.mu.Lock()
		b.turns = append(b.turns, brain.Turn{Role: "user", Content: input}, brain.Turn{Role: "assistant", Content: reply})
		b.mu.Unlock()
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *workingPersona) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", errors.New("not used")
}

func (b *workingPersona) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return brain.CloneTurns(b.turns)
}
func (b *workingPersona) ClearHistory() { b.LoadHistory(nil) }
func (b *workingPersona) LoadHistory(turns []brain.Turn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = brain.CloneTurns(turns)
}
func (b *workingPersona) ContinuitySnapshot() brain.ContinuitySnapshot {
	turns := b.History()
	return brain.ContinuitySnapshot{Canonical: turns, Working: brain.CloneTurns(turns)}
}
func (b *workingPersona) ImportContinuity(s brain.ContinuitySnapshot) error {
	b.LoadHistory(s.Canonical)
	return nil
}
func (b *workingPersona) AppendAssistantResponseWithIdentity(text string, identity brain.TurnIdentity) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = append(b.turns, brain.Turn{Role: "assistant", Content: text, PersonaID: identity.PersonaID, PersonaDisplayName: identity.PersonaDisplayName})
	return nil
}

func (b *workingPersona) holdErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.holdCtx == nil {
		return nil
	}
	return b.holdCtx.Err()
}

// twoPersonaChat is a real conversation screen over a pipeline whose Anika
// persona works on held lines and whose Obey persona is selected by a line
// starting "Hey Obey", as the persona selector does.
type twoPersonaChat struct {
	t            *testing.T
	p            *pipeline.Pipeline
	anika, obey  *workingPersona
	program      *tea.Program
	views        chan string
	replies      chan events.ResponseReady
	jobsFinished chan events.JobFinished
}

func newTwoPersonaChat(t *testing.T) *twoPersonaChat {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	c := &twoPersonaChat{t: t, anika: newWorkingPersona("Noted."), obey: newWorkingPersona("It is noon."),
		views: make(chan string, 1), replies: make(chan events.ResponseReady, 8), jobsFinished: make(chan events.JobFinished, 4)}
	bus := events.NewBus()
	c.p = &pipeline.Pipeline{Brain: c.anika, Events: bus}
	if err := c.p.BeginConversationIdentity("two-persona"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(ctx)
	pipeline.NewAgency(c.p, jobs, nil, &config.Config{AgentName: "Anika", ActivePersona: "anika"}, nil).Wire()
	c.p.NamesOtherPersona = func(input string) bool { return strings.HasPrefix(input, "Hey Obey") }
	c.p.AdmitInput = func(_ context.Context, input string, _ bool) (pipeline.ResponseAdmission, error) {
		if strings.HasPrefix(input, "Hey Obey") && c.p.Brain == brain.Provider(c.anika) {
			if err := c.p.ReplaceConversationBrain(c.obey, nil, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
				return pipeline.ResponseAdmission{}, err
			}
		}
		envelope := events.ResponseEnvelope{SessionID: "two-persona", ResponseID: input, PersonaID: "anika", DisplayName: "Anika"}
		if c.p.Brain == brain.Provider(c.obey) {
			envelope.PersonaID, envelope.DisplayName = "obey", "Obey"
		}
		return pipeline.ResponseAdmission{Request: input, Envelope: envelope}, nil
	}
	events.Subscribe(bus, func(e events.ResponseReady) { c.replies <- e })
	events.Subscribe(bus, func(e events.JobFinished) { c.jobsFinished <- e })

	var turns sync.WaitGroup
	h := &startedTerminal{recoveryTerminal: recoveryTerminal{model: sizedConversation(t, 100, 40), views: c.views},
		deps: conversationDeps{runner: c.p, bus: bus, ctx: ctx, wg: &turns, workScope: c.p.ConversationScope}}
	c.program = tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := c.program.Run(); done <- err }()
	c.program.Send(tea.WindowSizeMsg{Width: 100, Height: 40})
	t.Cleanup(func() {
		select {
		case <-c.anika.release:
		default:
			close(c.anika.release)
		}
		cancel()
		c.program.Quit()
		<-done
		turns.Wait()
		jobs.Close()
	})
	return c
}

// startedTerminal attaches the conversation screen to its pipeline's events
// as the app does when a conversation opens.
type startedTerminal struct {
	recoveryTerminal
	deps conversationDeps
}

func (h *startedTerminal) Init() tea.Cmd { return h.model.startConversation(h.deps) }
func (h *startedTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := h.recoveryTerminal.Update(msg)
	return h, cmd
}

func (c *twoPersonaChat) send(text string) {
	c.program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	c.program.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

func (c *twoPersonaChat) reply(persona string) events.ResponseReady {
	c.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-c.replies:
			if e.Envelope != nil && e.Envelope.PersonaID == persona && strings.TrimSpace(e.Response) != "" && !e.Interrupted {
				return e
			}
		case <-timeout:
			c.t.Fatalf("no reply from %s", persona)
			return events.ResponseReady{}
		}
	}
}

// waitView waits until the conversation screen shows text in a reply under
// speaker's name.
func (c *twoPersonaChat) waitView(speaker, text string) {
	c.t.Helper()
	timeout := time.After(10 * time.Second)
	last := ""
	for {
		select {
		case view := <-c.views:
			last = view
			if speakerOf(view, text) == speaker {
				return
			}
		case <-timeout:
			c.t.Fatalf("screen never showed %q under %s:\n%s", text, speaker, last)
		}
	}
}

// speakerOf names the reply on screen that holds text: the nearest heading
// above it.
func speakerOf(view, text string) string {
	at := strings.Index(view, text)
	if at < 0 {
		return ""
	}
	heading := strings.LastIndex(view[:at], "● ")
	if heading < 0 {
		return ""
	}
	name, _, _ := strings.Cut(view[heading+len("● "):], "\n")
	return strings.TrimSpace(name)
}

// Typing a message to Obey while Anika works leaves her turn running: Obey
// answers while her request is still open, and her reply arrives afterwards
// under her name.
func TestTypedMessageToAnotherPersonaLeavesTheWorkingTurnRunning(t *testing.T) {
	c := newTwoPersonaChat(t)
	c.send("Anika, HOLD and check the build")
	select {
	case <-c.anika.held:
	case <-time.After(10 * time.Second):
		t.Fatal("Anika's turn never started")
	}
	c.send("Hey Obey, what time is it?")
	obey := c.reply("obey")
	if obey.Response != "It is noon." {
		t.Fatalf("Obey replied %q", obey.Response)
	}
	if err := c.anika.holdErr(); err != nil {
		t.Fatalf("Anika's turn was cancelled when the message to Obey was sent: %v", err)
	}
	close(c.anika.release)
	select {
	case finished := <-c.jobsFinished:
		if finished.Envelope == nil || finished.Envelope.DisplayName != "Anika" || finished.Summary != "Found three files." {
			t.Fatalf("Anika's reply arrived as %+v", finished)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Anika's reply never arrived")
	}
	c.waitView("Anika", "Found three files.")
}

// A message naming the persona who is working, or naming nobody, stops her
// turn as before.
func TestTypedMessageNotNamingAnotherPersonaStopsTheWorkingTurn(t *testing.T) {
	c := newTwoPersonaChat(t)
	c.send("Anika, HOLD and check the build")
	select {
	case <-c.anika.held:
	case <-time.After(10 * time.Second):
		t.Fatal("Anika's turn never started")
	}
	c.send("Anika, stop that.")
	deadline := time.Now().Add(10 * time.Second)
	for c.anika.holdErr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("Anika's turn was not stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reply := c.reply("anika"); reply.Response != "Noted." {
		t.Fatalf("Anika replied %q to the next message", reply.Response)
	}
	select {
	case finished := <-c.jobsFinished:
		t.Fatalf("a job finished: %+v", finished)
	default:
	}
}
