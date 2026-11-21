package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// workingBrain keeps the shared history. A line with "HOLD" in it gets a first
// sentence and a tool call, then waits for release before it runs a second
// tool and finishes; fail, when set, ends that request with an error instead.
// Any other line is answered at once.
type workingBrain struct {
	release chan struct{}
	held    chan struct{}
	fail    error

	mu        sync.Mutex
	turns     []brain.Turn
	requests  []string
	holdCtx   context.Context
	holdEnded chan struct{}
	stopped   int
}

func newWorkingBrain() *workingBrain {
	return &workingBrain{release: make(chan struct{}), held: make(chan struct{}), holdEnded: make(chan struct{})}
}

func (b *workingBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.requests = append(b.requests, input)
	b.mu.Unlock()
	out, done := make(chan string), make(chan brain.StreamResult, 1)
	send := func(text string) bool {
		select {
		case out <- text:
			return true
		case <-ctx.Done():
			return false
		}
	}
	record := func(reply string) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.turns = append(b.turns, brain.Turn{Role: "user", Content: opts.OriginalInput})
		b.turns = append(b.turns, assistantTurn(reply, opts.Identity, time.Now()))
	}
	go func() {
		defer close(done)
		defer close(out)
		if !strings.Contains(input, "HOLD") {
			if send("Noted.") {
				record("Noted.")
				done <- brain.StreamResult{}
			} else {
				done <- brain.StreamResult{Err: ctx.Err()}
			}
			return
		}
		defer close(b.holdEnded)
		if !send("Working on it. ") {
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		if opts.OnToolStart != nil {
			opts.OnToolStart(brain.ToolCall{ID: "call-1", Name: "list_files"})
		}
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: brain.ToolCall{ID: "call-1", Name: "list_files"}})
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
		if opts.OnToolStart != nil {
			opts.OnToolStart(brain.ToolCall{ID: "call-2", Name: "read_file"})
		}
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: brain.ToolCall{ID: "call-2", Name: "read_file"}})
		}
		if b.fail != nil {
			done <- brain.StreamResult{Err: b.fail}
			return
		}
		if !send("Found three files.") {
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		record("Working on it. Found three files.")
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *workingBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", errors.New("not used")
}

func (b *workingBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return brain.CloneTurns(b.turns)
}

func (b *workingBrain) ClearHistory() { b.LoadHistory(nil) }

func (b *workingBrain) LoadHistory(turns []brain.Turn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = brain.CloneTurns(turns)
}

func (b *workingBrain) ContinuitySnapshot() brain.ContinuitySnapshot {
	turns := b.History()
	return brain.ContinuitySnapshot{Canonical: turns, Working: brain.CloneTurns(turns)}
}

func (b *workingBrain) ImportContinuity(s brain.ContinuitySnapshot) error {
	b.LoadHistory(s.Canonical)
	return nil
}

func (b *workingBrain) AppendAssistantResponseWithIdentity(text string, identity brain.TurnIdentity) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = append(b.turns, assistantTurn(text, &identity, time.Now()))
	return nil
}

func (b *workingBrain) RecordStoppedRequest(s brain.StoppedRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped++
	for _, t := range []*brain.Turn{s.Line, s.Reply} {
		if t != nil {
			b.turns = append(b.turns, *t)
		}
	}
}

// holdErr is the held request's context error once it has been held.
func (b *workingBrain) holdErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.holdCtx == nil {
		return nil
	}
	return b.holdCtx.Err()
}

func (b *workingBrain) requestLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

// movingConversation is a conversation whose persona Anika is on a held
// brain, with Obey named by any line starting "Hey Obey".
type movingConversation struct {
	t      *testing.T
	p      *Pipeline
	jobs   *job.Table
	anika  *workingBrain
	turn   chan error
	events chan events.Event
}

func newMovingConversation(t *testing.T) *movingConversation {
	t.Helper()
	c := &movingConversation{t: t, anika: newWorkingBrain(), turn: make(chan error, 1), events: make(chan events.Event, 64)}
	c.p = &Pipeline{Brain: c.anika, Events: events.NewBus()}
	if err := c.p.BeginConversationIdentity("s"); err != nil {
		t.Fatal(err)
	}
	c.p.AdmitInput = admitPersona("anika", "Anika")
	c.p.NamesOtherPersona = func(input string) bool { return strings.HasPrefix(input, "Hey Obey") }
	c.jobs = job.NewTable(t.Context())
	t.Cleanup(c.jobs.Close)
	NewAgency(c.p, c.jobs, nil, &config.Config{AgentName: "Anika", ActivePersona: "anika"}, nil).Wire()
	keep := func(e events.Event) {
		select {
		case c.events <- e:
		default:
		}
	}
	events.Subscribe(c.p.Events, func(e events.JobFinished) { keep(e) })
	events.Subscribe(c.p.Events, func(e events.JobFailed) { keep(e) })
	events.Subscribe(c.p.Events, func(e events.JobAborted) { keep(e) })
	events.Subscribe(c.p.Events, func(e events.ToolCallFinished) { keep(e) })
	return c
}

func admitPersona(id, name string) func(context.Context, string, bool) (ResponseAdmission, error) {
	return func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{SessionID: "s", ResponseID: id + "-" + input, PersonaID: id, DisplayName: name, PromptHash: id + "-prompt"}}, nil
	}
}

// holdTurn starts Anika's held turn and waits until her request is held
// after its first sentence and first tool.
func (c *movingConversation) holdTurn() {
	c.t.Helper()
	go func() { c.turn <- c.p.RunTurnTextMode(c.t.Context(), "Anika, HOLD and check the build") }()
	select {
	case <-c.anika.held:
	case <-time.After(5 * time.Second):
		c.t.Fatal("Anika's request never started")
	}
}

// move moves Anika's turn and waits for it to return.
func (c *movingConversation) move() {
	c.t.Helper()
	if !c.p.MoveTurnToBackground("Hey Obey, what time is it?") {
		c.t.Fatal("the running turn was not moved to the background")
	}
	select {
	case err := <-c.turn:
		if err != nil {
			c.t.Fatalf("moved turn returned %v", err)
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("moved turn did not return")
	}
}

func (c *movingConversation) movedJob() job.Info {
	c.t.Helper()
	var running []job.Info
	for _, info := range c.jobs.Status() {
		if info.State == job.StateRunning {
			running = append(running, info)
		}
	}
	if len(running) != 1 {
		c.t.Fatalf("jobs after the move = %+v", c.jobs.Status())
	}
	return running[0]
}

func (c *movingConversation) next(match func(events.Event) bool) events.Event {
	c.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-c.events:
			if match(e) {
				return e
			}
		case <-timeout:
			c.t.Fatal("timed out waiting for a job event")
			return nil
		}
	}
}

// A message naming another persona while Anika's turn is running moves her
// request to a background job. It is not cancelled, the job carries her
// identity, her later tool runs belong to the job, and when the request ends
// its remaining text is her job's result, written once to the history of the
// persona answering by then.
func TestMovedTurnKeepsItsRequestAndDeliversItAsAJob(t *testing.T) {
	c := newMovingConversation(t)
	c.holdTurn()
	c.move()
	if err := c.anika.holdErr(); err != nil {
		t.Fatalf("the moved request was stopped: %v", err)
	}
	info := c.movedJob()
	c.p.Agency.mu.Lock()
	origin := c.p.Agency.workerOrigins[info.ID]
	c.p.Agency.mu.Unlock()
	if origin.Envelope.PersonaID != "anika" || origin.Envelope.DisplayName != "Anika" {
		t.Fatalf("moved job origin = %+v", origin.Envelope)
	}

	obey := newWorkingBrain()
	if err := c.p.ReplaceConversationBrain(obey, nil, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
		t.Fatal(err)
	}
	close(c.anika.release)
	tool := c.next(func(e events.Event) bool { f, ok := e.(events.ToolCallFinished); return ok && f.ID == "call-2" }).(events.ToolCallFinished)
	if tool.JobID != info.ID {
		t.Fatalf("a tool run after the move belongs to job %q, want %q", tool.JobID, info.ID)
	}
	finished := c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok }).(events.JobFinished)
	if finished.ID != info.ID || finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || finished.Summary != "Found three files." {
		t.Fatalf("job finished = %+v", finished)
	}
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	var replies []brain.Turn
	for _, turn := range obey.History() {
		if turn.Role == "assistant" && strings.Contains(turn.Content, "Found three files.") {
			replies = append(replies, turn)
		}
	}
	if len(replies) != 1 || replies[0].PersonaID != "anika" || replies[0].Content != "Found three files." {
		t.Fatalf("Obey's history holds Anika's result as %+v", obey.History())
	}
}
