package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// workingBrain keeps the shared history. A line with "HOLD" in it gets a
// first sentence and a tool call, then waits for release before it runs a
// second tool and finishes; fail, when set, ends that request with an error
// instead, and route with that route; last replaces its final text, sent
// after trickle more sentences gap apart. Any other line is answered after
// steps words gap apart.
type workingBrain struct {
	release chan struct{}
	held    chan struct{}
	fail    error
	route   string
	// failText is sent before the request ends with fail, reported as
	// recovered when recovered is set; noResult ends it with no result.
	failText  string
	recovered bool
	noResult  bool
	// none ends the held request with no more text; stubborn holds it until
	// release even after it is stopped.
	none, stubborn bool
	before         []string
	last           string
	trickle        int
	steps          int
	gap            time.Duration

	mu        sync.Mutex
	turns     []brain.Turn
	requests  []string
	holdCtx   context.Context
	holdEnded chan struct{}
	stopped   int
	// ended closes when the test ends, so no request outlives it however
	// the test ended.
	ended   chan struct{}
	endOnce sync.Once
}

func newWorkingBrain() *workingBrain {
	return &workingBrain{release: make(chan struct{}), held: make(chan struct{}), holdEnded: make(chan struct{}), ended: make(chan struct{})}
}

// end stops every request still running when the test ends.
func (b *workingBrain) end() { b.endOnce.Do(func() { close(b.ended) }) }

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
		case <-b.ended:
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
			for range b.steps {
				time.Sleep(b.gap)
				if !send("word ") {
					done <- brain.StreamResult{Err: ctx.Err()}
					return
				}
			}
			if send("Noted.") {
				record("Noted.")
				done <- brain.StreamResult{}
			} else {
				done <- brain.StreamResult{Err: ctx.Err()}
			}
			return
		}
		defer close(b.holdEnded)
		before := b.before
		if before == nil {
			before = []string{"Working on it. "}
		}
		for _, chunk := range before {
			if !send(chunk) {
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			}
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
			if b.stubborn {
				select {
				case <-b.release:
				case <-b.ended:
				}
			}
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		case <-b.release:
		case <-b.ended:
			done <- brain.StreamResult{Err: context.Canceled}
			return
		}
		if opts.OnToolStart != nil {
			opts.OnToolStart(brain.ToolCall{ID: "call-2", Name: "read_file"})
		}
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: brain.ToolCall{ID: "call-2", Name: "read_file"}})
		}
		if b.fail != nil || b.noResult {
			if b.failText != "" && !send(b.failText) {
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			}
			if !b.noResult {
				done <- brain.StreamResult{Err: b.fail, Recovered: b.recovered, Route: b.route}
			}
			return
		}
		if b.route != "" {
			done <- brain.StreamResult{Route: b.route}
			return
		}
		for range b.trickle {
			time.Sleep(b.gap)
			if !send("Still working. ") {
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			}
		}
		last := b.last
		if last == "" && !b.none {
			last = "Found three files."
		}
		if !send(last) {
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		record(strings.Join(before, "") + last)
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

func (b *workingBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.turns) - 1; i >= 0; i-- {
		if b.turns[i].Role == "assistant" && b.turns[i].Content == text {
			b.turns[i].Attachments = slices.Clone(refs)
			return nil
		}
	}
	return errors.New("no such reply")
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
	readMu sync.Mutex
	read   strings.Builder
	errs   []events.Error
	t      *testing.T
	p      *Pipeline
	jobs   *job.Table
	anika  *workingBrain
	turn   chan error
	events chan events.Event
}

func newMovingConversation(t *testing.T, opts ...job.Option) *movingConversation {
	t.Helper()
	c := &movingConversation{t: t, anika: newWorkingBrain(), turn: make(chan error, 1), events: make(chan events.Event, 64)}
	c.p = &Pipeline{Brain: c.anika, Events: events.NewBus()}
	if err := c.p.BeginConversationIdentity("s"); err != nil {
		t.Fatal(err)
	}
	c.p.AdmitInput = admitPersona("anika", "Anika")
	c.p.NamesOtherPersona = func(input string) bool { return strings.HasPrefix(input, "Hey Obey") }
	c.jobs = job.NewTable(t.Context(), opts...)
	t.Cleanup(c.jobs.Close)
	t.Cleanup(c.anika.end)
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
	events.Subscribe(c.p.Events, func(e events.ResponseReady) { keep(e) })
	events.Subscribe(c.p.Events, func(e events.ResponseDelta) {
		c.readMu.Lock()
		c.read.WriteString(e.Text)
		c.readMu.Unlock()
	})
	events.Subscribe(c.p.Events, func(e events.TurnMetrics) { keep(e) })
	events.Subscribe(c.p.Events, func(e events.Error) {
		c.readMu.Lock()
		c.errs = append(c.errs, e)
		c.readMu.Unlock()
	})
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
	c.waitRead()
}

// waitRead waits until the turn has read everything Anika sent before she
// was held.
func (c *movingConversation) waitRead() {
	c.t.Helper()
	want := strings.Join(c.anika.before, "")
	if c.anika.before == nil {
		want = "Working on it. "
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.readMu.Lock()
		read := c.read.String()
		c.readMu.Unlock()
		if read == want {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("the turn read %q, want %q", read, want)
		}
		time.Sleep(time.Millisecond)
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
	if finished.ID != info.ID || finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || finished.Summary != " Found three files." {
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

func (c *movingConversation) waitHoldErr() error {
	c.t.Helper()
	select {
	case <-c.anika.holdEnded:
	case <-time.After(5 * time.Second):
		c.t.Fatal("Anika's held request never ended")
	}
	return c.anika.holdErr()
}

// Stopping the job a turn was moved to stops the model request.
func TestAbortingAMovedJobStopsItsRequest(t *testing.T) {
	c := newMovingConversation(t)
	c.holdTurn()
	c.move()
	c.jobs.Abort(c.movedJob().ID)
	if err := c.waitHoldErr(); !errors.Is(err, context.Canceled) {
		t.Fatalf("held request ended with %v after its job was stopped", err)
	}
	c.next(func(e events.Event) bool { _, ok := e.(events.JobAborted); return ok })
}

// A moved request that fails reports a failed job.
func TestMovedRequestThatFailsReportsAFailedJob(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.fail = errors.New("model crashed")
	c.holdTurn()
	c.move()
	id := c.movedJob().ID
	close(c.anika.release)
	failed := c.next(func(e events.Event) bool { _, ok := e.(events.JobFailed); return ok }).(events.JobFailed)
	if failed.ID != id || !strings.Contains(failed.Err, "model crashed") {
		t.Fatalf("job failed = %+v", failed)
	}
}

// Closing the conversation stops a moved request, whether or not another
// persona took over, and does not wait for it to end on its own.
func TestClosingTheConversationStopsAMovedRequest(t *testing.T) {
	for _, switched := range []bool{false, true} {
		c := newMovingConversation(t)
		c.holdTurn()
		c.move()
		if switched {
			if err := c.p.ReplaceConversationBrain(newWorkingBrain(), nil, &config.Config{}, nil); err != nil {
				t.Fatal(err)
			}
		}
		closed := make(chan error, 1)
		go func() { closed <- c.p.CloseBrains() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("switched=%v: closing waited for the moved request", switched)
		}
		if err := c.waitHoldErr(); !errors.Is(err, context.Canceled) {
			t.Fatalf("switched=%v: held request ended with %v", switched, err)
		}
	}
}

// When the next turn stays on the provider the moved request runs on, that
// request is stopped and recorded as a stopped turn is, before the provider
// takes the next one.
func TestNextTurnOnTheSameProviderStopsTheMovedRequest(t *testing.T) {
	c := newMovingConversation(t)
	c.holdTurn()
	c.move()
	if err := c.p.RunTurnTextMode(t.Context(), "Hey Obey, what time is it?"); err != nil {
		t.Fatal(err)
	}
	if err := c.waitHoldErr(); !errors.Is(err, context.Canceled) {
		t.Fatalf("held request ended with %v", err)
	}
	c.next(func(e events.Event) bool { _, ok := e.(events.JobAborted); return ok })
	if requests := c.anika.requestLog(); len(requests) != 2 {
		t.Fatalf("requests = %q", requests)
	}
	c.anika.mu.Lock()
	stopped := c.anika.stopped
	c.anika.mu.Unlock()
	if stopped != 1 {
		t.Fatalf("the stopped request was recorded %d times", stopped)
	}
}

// A turn is not moved for a line that names nobody else, nor on a provider
// whose request cannot be moved; the caller stops it as before.
func TestTurnIsNotMovedUnlessItCanBe(t *testing.T) {
	t.Run("names nobody else", func(t *testing.T) {
		c := newMovingConversation(t)
		c.holdTurn()
		if c.p.MoveTurnToBackground("Anika, stop that.") {
			t.Fatal("a line naming the working persona moved her turn")
		}
		close(c.anika.release)
		if err := <-c.turn; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("provider keeps no shared history", func(t *testing.T) {
		transcript := &recordedTranscript{}
		waiting := &waitingBrain{started: make(chan struct{})}
		p := &Pipeline{Brain: waiting, Events: events.NewBus(), Transcript: transcript}
		if err := p.BeginConversationIdentity("s"); err != nil {
			t.Fatal(err)
		}
		p.AdmitInput = admitPersona("anika", "Anika")
		p.NamesOtherPersona = func(string) bool { return true }
		jobs := job.NewTable(t.Context())
		defer jobs.Close()
		NewAgency(p, jobs, nil, &config.Config{}, nil).Wire()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- p.RunTurnTextMode(ctx, "check the build") }()
		<-waiting.started
		if p.MoveTurnToBackground("Hey Obey, what time is it?") {
			t.Fatal("a request on a provider without shared history was moved")
		}
		cancel()
		if err := <-done; err == nil {
			t.Fatal("the stopped turn reported success")
		}
	})
}

// waitingBrain keeps no shared history; its reply starts and then waits until
// it is stopped.
type waitingBrain struct {
	fakeBrain
	started chan struct{}
}

func (b *waitingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	out <- "Working on it. "
	close(b.started)
	go func() {
		defer close(done)
		defer close(out)
		<-ctx.Done()
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// A spoken turn moved while it speaks stops speaking at once, and its result
// is later spoken in the voice of the persona that started it, not in the
// voice of the persona selected since.
func TestMovedVoiceTurnSpeaksItsResultInItsOwnVoice(t *testing.T) {
	c := newMovingConversation(t)
	anikaVoice, obeyVoice := &fakeTTS{}, &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	c.p.ReplaceTTS(anikaVoice, nil)
	c.p.Player = player
	c.p.STT = &fakeSTT{text: "Anika, HOLD and check the build"}
	go func() {
		_, err := c.p.RunTurn(t.Context())
		c.turn <- err
	}()
	select {
	case <-c.anika.held:
	case <-time.After(5 * time.Second):
		t.Fatal("Anika's request never started")
	}
	c.move()
	if err := c.anika.holdErr(); err != nil {
		t.Fatalf("the moved request was stopped: %v", err)
	}
	c.p.ReplaceTTS(obeyVoice, nil)
	if err := c.p.ReplaceConversationBrain(newWorkingBrain(), nil, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	close(c.anika.release)
	c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok })
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(anikaVoice.Texts(), " "), "Found three files.") {
		if time.Now().After(deadline) {
			t.Fatalf("Anika's voice said %q; Obey's said %q", anikaVoice.Texts(), obeyVoice.Texts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(strings.Join(obeyVoice.Texts(), " "), "Found three files.") {
		t.Fatalf("Obey's voice spoke Anika's result: %q", obeyVoice.Texts())
	}
}

// A moved reply that ends by handing its work off runs that work in the same
// job, on the worker of the persona that started it, not the worker of the
// persona selected since, and its result is delivered under that persona.
func TestMovedReplyThatHandsOffRunsTheWorkForItsPersona(t *testing.T) {
	c := newMovingConversation(t)
	anikaWorker := &recordingWorker{reply: "Checked by Anika's worker."}
	c.p.Agency.ReplaceWorker(anikaWorker, &config.Config{AgentName: "Anika", ActivePersona: "anika"}, nil)
	c.anika.route = "spawn"
	c.holdTurn()
	c.move()
	id := c.movedJob().ID
	obeyWorker := &recordingWorker{reply: "Checked by Obey's worker."}
	if err := c.p.ReplaceConversationBrain(newWorkingBrain(), obeyWorker, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
		t.Fatal(err)
	}
	close(c.anika.release)
	e := c.next(func(e events.Event) bool {
		switch e.(type) {
		case events.JobFinished, events.JobFailed:
			return true
		}
		return false
	})
	finished, ok := e.(events.JobFinished)
	if !ok || finished.ID != id || finished.Envelope == nil || finished.Envelope.PersonaID != "anika" || finished.Summary != "Checked by Anika's worker." {
		t.Fatalf("the handed-off work ended as %+v", e)
	}
	if anikaWorker.calls() != 1 || obeyWorker.calls() != 0 || !strings.Contains(anikaWorker.briefs[0], "check the build") {
		t.Fatalf("Anika's worker ran %d jobs (%q), Obey's %d", anikaWorker.calls(), anikaWorker.briefs, obeyWorker.calls())
	}
}

// A moved reply that ends by asking for narration, which needs the live
// conversation, fails its job with a reason instead of losing the request.
func TestMovedReplyThatAsksForNarrationFailsItsJob(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.route = "narrate"
	c.holdTurn()
	c.move()
	close(c.anika.release)
	failed := c.next(func(e events.Event) bool { _, ok := e.(events.JobFailed); return ok }).(events.JobFailed)
	if !strings.Contains(failed.Err, "narrat") {
		t.Fatalf("job failed = %+v", failed)
	}
}

// A moved reply that goes silent ends as a failed job after the idle time a
// live reply gets, and its request is stopped.
func TestMovedReplyThatGoesSilentFailsItsJob(t *testing.T) {
	c := newMovingConversation(t)
	c.p.BrainTurnTimeout = 150 * time.Millisecond
	c.holdTurn()
	c.move()
	failed := c.next(func(e events.Event) bool { _, ok := e.(events.JobFailed); return ok }).(events.JobFailed)
	if !strings.Contains(failed.Err, "sent nothing") {
		t.Fatalf("job failed = %+v", failed)
	}
	if err := c.waitHoldErr(); !errors.Is(err, context.Canceled) {
		t.Fatalf("held request ended with %v", err)
	}
}

// A moved reply that keeps producing runs past the idle time, and past a job
// time limit, which applies only to work it hands off: moving a reply does
// not shorten how long its persona may work.
func TestMovedReplyRunsWhileItIsActive(t *testing.T) {
	c := newMovingConversation(t, job.WithBudget(100*time.Millisecond))
	c.p.BrainTurnTimeout = 200 * time.Millisecond
	c.anika.trickle, c.anika.gap = 6, 80*time.Millisecond
	c.holdTurn()
	c.move()
	time.Sleep(50 * time.Millisecond)
	close(c.anika.release)
	e := c.next(func(e events.Event) bool {
		switch e.(type) {
		case events.JobFinished, events.JobFailed, events.JobAborted:
			return true
		}
		return false
	})
	if finished, ok := e.(events.JobFinished); !ok || !strings.HasSuffix(finished.Summary, "Found three files.") {
		t.Fatalf("an active moved reply ended as %+v", e)
	}
}

// An image line in a moved reply becomes an attachment of the job's result,
// as it does in a worker's answer: it is not shown or spoken as a path, and
// it is saved with the reply.
func TestMovedReplyWithAnImageLineAttachesTheImage(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	if err := os.WriteFile(filepath.Join(root, "chart.gif"), testmedia.GIF(), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newMovingConversation(t)
	c.p.Agency.ReplaceWorker(nil, &config.Config{AgentName: "Anika", ActivePersona: "anika", WorkDir: root}, nil)
	c.anika.last = "Here is the chart.\n![chart](chart.gif)"
	c.holdTurn()
	c.move()
	obey := newWorkingBrain()
	if err := c.p.ReplaceConversationBrain(obey, nil, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
		t.Fatal(err)
	}
	close(c.anika.release)
	finished := c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok }).(events.JobFinished)
	if len(finished.Attachments) != 1 || strings.Contains(finished.Summary, "![") || strings.Contains(finished.Summary, "chart.gif") || !strings.Contains(finished.Summary, "Here is the chart.") {
		t.Fatalf("job finished = %+v", finished)
	}
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	history := obey.History()
	if len(history) == 0 {
		t.Fatal("the reply was not saved")
	}
	reply := history[len(history)-1]
	if reply.PersonaID != "anika" || len(reply.Attachments) != 1 || strings.Contains(reply.Content, "![") {
		t.Fatalf("saved reply = %+v", reply)
	}
}

// Whether a turn moves is decided on the turn's goroutine, which owns the
// persona selector during the turn: the hook below shares unguarded state
// with the turn's end. A move asked for while the turn streams, finishes or
// has ended is answered promptly either way; a refused one leaves the turn
// to finish.
func TestMoveIsDecidedOnTheTurnsOwnGoroutine(t *testing.T) {
	for i := range 20 {
		c := newMovingConversation(t)
		c.anika.steps, c.anika.gap = 5, time.Duration(i%4)*time.Millisecond
		calls := 0
		events.Subscribe(c.p.Events, func(events.ResponseReady) { calls++ })
		c.p.NamesOtherPersona = func(input string) bool {
			calls++
			return strings.HasPrefix(input, "Hey Obey")
		}
		line := "Anika, keep going."
		if i%2 == 1 {
			line = "Hey Obey, what time is it?"
		}
		go func() { c.turn <- c.p.RunTurnTextMode(t.Context(), "Anika, check the build") }()
		moved, ended := false, false
		for !moved && !ended {
			select {
			case err := <-c.turn:
				if err != nil {
					t.Fatal(err)
				}
				ended = true
				continue
			default:
			}
			asked := time.Now()
			moved = c.p.MoveTurnToBackground(line)
			if took := time.Since(asked); took > moveOfferWait+time.Second {
				t.Fatalf("asking to move took %v", took)
			}
		}
		if moved != (i%2 == 1) && !(i%2 == 1 && ended) {
			t.Fatalf("line %q moved=%v", line, moved)
		}
		if moved {
			if err := <-c.turn; err != nil {
				t.Fatalf("moved turn returned %v", err)
			}
			c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok })
		}
		if c.p.MoveTurnToBackground("Hey Obey, what time is it?") {
			t.Fatal("a move was accepted with no turn running")
		}
	}
}

// A moved reply is divided at a sentence end: what the turn keeps ends a
// sentence, the job's reply begins one, and together, whitespace included,
// they are the model's reply.
func TestMovedReplyIsDividedAtASentenceEnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     []string
		last, kept string
	}{
		{"mid-word", []string{"Lighthouses guided ships. Stories were told by travel"}, "ers from Pliny onward.", "Lighthouses guided ships."},
		{"at a sentence end", []string{"One is first. Two is second. "}, "Three is last.", "One is first. Two is second."},
		{"sentence end not yet confirmed", []string{"One is first. Two is second."}, " Three is last.", "One is first."},
		{"before any sentence", []string{"Lighthouses are"}, " tall towers.", ""},
		{"paragraph break", []string{"The first paragraph ends here.\n\nThe second starts"}, " and ends.", "The first paragraph ends here."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMovingConversation(t)
			c.anika.before, c.anika.last = tc.before, tc.last
			c.holdTurn()
			c.move()
			id := c.movedJob().ID
			ready := c.next(func(e events.Event) bool { _, ok := e.(events.ResponseReady); return ok }).(events.ResponseReady)
			if ready.Response != tc.kept || ready.ContinuedAs != id || ready.Interrupted {
				t.Fatalf("the turn ended with %+v, want %q continued as %s", ready, tc.kept, id)
			}
			metrics := c.next(func(e events.Event) bool { _, ok := e.(events.TurnMetrics); return ok }).(events.TurnMetrics)
			if metrics.Outcome != TurnMoved.String() || metrics.Interrupted {
				t.Fatalf("turn outcome %q interrupted=%v", metrics.Outcome, metrics.Interrupted)
			}
			history, ok := c.p.MovedContinuity(c.anika)
			if !ok {
				t.Fatal("no history kept for the moved reply")
			}
			// With nothing kept, the history records the request as handed off.
			want := tc.kept
			if want == "" {
				want = spawnAcknowledgement
			}
			kept := 0
			for _, turn := range history.Canonical {
				if turn.Role == "assistant" {
					kept++
					if turn.Content != want {
						t.Fatalf("history keeps %q, want %q", turn.Content, want)
					}
				}
			}
			if kept != 1 {
				t.Fatalf("history holds %d replies to the moved request: %+v", kept, history.Canonical)
			}
			close(c.anika.release)
			finished := c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok }).(events.JobFinished)
			full := strings.Join(tc.before, "") + tc.last
			if ready.Response+finished.Summary != full {
				t.Fatalf("kept %q + delivered %q != %q", ready.Response, finished.Summary, full)
			}
		})
	}
}

// A spoken reply keeps only the sentences it finished saying. A sentence cut
// off by the move is delivered again in full, in the persona's voice.
func TestMovedVoiceReplyKeepsWhatItFinishedSaying(t *testing.T) {
	for _, tc := range []struct {
		name string
		play time.Duration
		kept string
	}{
		{"finished saying", time.Millisecond, "One is first. Two is second."},
		{"cut off while saying", time.Minute, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMovingConversation(t)
			voice := &fakeTTS{}
			player := newFakePlayer(tc.play)
			defer player.Close()
			c.p.ReplaceTTS(voice, nil)
			c.p.Player = player
			c.p.STT = &fakeSTT{text: "Anika, HOLD and check the build"}
			c.anika.before, c.anika.last = []string{"One is first. Two is second. "}, "Three is last."
			// The turn counts a group as said on the loop pass that reports it
			// finished, so the move it takes afterwards sees the count.
			var mu sync.Mutex
			started, said := 0, 0
			events.Subscribe(c.p.Events, func(events.SpeakingStarted) { mu.Lock(); started++; mu.Unlock() })
			events.Subscribe(c.p.Events, func(e events.SpeakingComplete) {
				if !e.Interrupted {
					mu.Lock()
					said++
					mu.Unlock()
				}
			})
			go func() {
				_, err := c.p.RunTurn(t.Context())
				c.turn <- err
			}()
			<-c.anika.held
			c.waitRead()
			deadline := time.Now().Add(5 * time.Second)
			for {
				texts := voice.Texts()
				mu.Lock()
				ready := started > 0
				if tc.kept != "" {
					ready = strings.Contains(strings.Join(texts, " "), "Two is second.") && said == len(texts)
				}
				mu.Unlock()
				if ready {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("speech never reached the point to move at: %q", texts)
				}
				time.Sleep(time.Millisecond)
			}
			c.move()
			ready := c.next(func(e events.Event) bool { _, ok := e.(events.ResponseReady); return ok }).(events.ResponseReady)
			if ready.Response != tc.kept {
				t.Fatalf("the turn kept %q, want %q", ready.Response, tc.kept)
			}
			close(c.anika.release)
			finished := c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok }).(events.JobFinished)
			if ready.Response+finished.Summary != "One is first. Two is second. Three is last." {
				t.Fatalf("kept %q + delivered %q", ready.Response, finished.Summary)
			}
		})
	}
}

// endMovedReply moves Anika's held reply, switches to Obey with voices of
// their own, releases the reply, and returns how its job ended and Obey's
// provider, which takes what the job delivers.
func endMovedReply(t *testing.T, c *movingConversation) (events.Event, *workingBrain, *fakeTTS) {
	t.Helper()
	anikaVoice := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	c.p.ReplaceTTS(anikaVoice, nil)
	c.p.Player = player
	c.holdTurn()
	c.move()
	obey := newWorkingBrain()
	if err := c.p.ReplaceConversationBrain(obey, nil, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
		t.Fatal(err)
	}
	c.p.ReplaceTTS(&fakeTTS{}, nil)
	close(c.anika.release)
	ended := c.next(func(e events.Event) bool {
		switch e.(type) {
		case events.JobFinished, events.JobFailed, events.JobAborted:
			return true
		}
		return false
	})
	return ended, obey, anikaVoice
}

func (c *movingConversation) errorEvent(t *testing.T, contains string) events.Error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.readMu.Lock()
		errs := slices.Clone(c.errs)
		c.readMu.Unlock()
		for _, err := range errs {
			if strings.Contains(err.Message, contains) {
				return err
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no error mentioning %q among %+v", contains, errs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A moved reply that fails partway after some text arrived, whether the
// provider recovered or not, delivers that text as its persona's unfinished
// reply, speaks it in her voice, saves it marked unfinished, and reports why.
func TestMovedReplyCutShortDeliversWhatArrivedAndSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recovered bool
		noResult  bool
		reason    string
	}{
		{"recovered", true, false, "stream broke"},
		{"not recovered", false, false, "stream broke"},
		{"ended without a result", false, true, "without a result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMovingConversation(t)
			if !tc.noResult {
				c.anika.fail = errors.New("stream broke")
			}
			c.anika.noResult, c.anika.recovered, c.anika.failText = tc.noResult, tc.recovered, "Found three"
			ended, obey, voice := endMovedReply(t, c)
			finished, ok := ended.(events.JobFinished)
			if !ok || !finished.CutShort || finished.Summary != " Found three" || finished.Envelope.PersonaID != "anika" {
				t.Fatalf("the cut-short reply ended as %+v", ended)
			}
			if err := c.errorEvent(t, tc.reason); !strings.Contains(err.Message, finished.ID) || !strings.Contains(err.Message, "cut short") {
				t.Fatalf("reason reported as %+v", err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !slices.Contains(voice.Texts(), "Found three") {
				if time.Now().After(deadline) {
					t.Fatalf("Anika's voice said %q", voice.Texts())
				}
				time.Sleep(5 * time.Millisecond)
			}
			var history []brain.Turn
			for len(history) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("the delivered reply was never saved")
				}
				if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
					t.Fatal(err)
				}
				history = obey.History()
				time.Sleep(5 * time.Millisecond)
			}
			saved := history[len(history)-1]
			if saved.PersonaID != "anika" || saved.Content != "Found three" || saved.Disposition != brain.IncompleteDisposition(errors.New("x")) {
				t.Fatalf("saved reply = %+v", saved)
			}
		})
	}
}

// A moved reply that fails before any more text arrives is a failed job
// with the reason, not a reply with nothing to report.
func TestMovedReplyFailingWithNoTextFailsItsJob(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.fail, c.anika.recovered = errors.New("stream broke"), true
	ended, obey, _ := endMovedReply(t, c)
	failed, ok := ended.(events.JobFailed)
	if !ok || !strings.Contains(failed.Err, "stream broke") {
		t.Fatalf("the failed reply ended as %+v", ended)
	}
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if history := obey.History(); len(history) != 0 {
		t.Fatalf("a failed reply was saved: %+v", history)
	}
}

// A moved reply that hands its work off and reports a recovered error does
// the work and reports the error.
func TestMovedHandOffWithARecoveredErrorDoesTheWorkAndSaysWhy(t *testing.T) {
	c := newMovingConversation(t)
	worker := &recordingWorker{reply: "Checked by Anika's worker."}
	c.p.Agency.ReplaceWorker(worker, &config.Config{AgentName: "Anika", ActivePersona: "anika"}, nil)
	c.anika.route, c.anika.fail, c.anika.recovered = "spawn", errors.New("stream broke"), true
	c.holdTurn()
	c.move()
	close(c.anika.release)
	c.errorEvent(t, "stream broke")
	finished := c.next(func(e events.Event) bool { _, ok := e.(events.JobFinished); return ok }).(events.JobFinished)
	if finished.Summary != "Checked by Anika's worker." || finished.Envelope.PersonaID != "anika" || worker.calls() != 1 {
		t.Fatalf("the handed-off work ended as %+v after %d worker runs", finished, worker.calls())
	}
}

// A moved reply that ends without any text, before or after the move, is a
// failed job rather than a reply with nothing to report.
func TestMovedReplyWithNoTextAtAllFailsItsJob(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.before, c.anika.last = []string{" "}, "\n"
	ended, _, _ := endMovedReply(t, c)
	if failed, ok := ended.(events.JobFailed); !ok || !strings.Contains(failed.Err, "without any text") {
		t.Fatalf("an empty moved reply ended as %+v", ended)
	}
}

// A moved reply of an inline persona is still its own reply finishing, and is
// recorded as inline; a hand-off at its end starts no worker.
func TestMovedReplyOfAnInlinePersonaHandsNothingOff(t *testing.T) {
	c := newMovingConversation(t)
	worker := &recordingWorker{reply: "Checked by the worker."}
	c.p.Agency.ReplaceWorker(worker, &config.Config{AgentName: "Anika", ActivePersona: "anika", PersonaWork: config.WorkInline}, nil)
	c.anika.route = "spawn"
	c.holdTurn()
	c.move()
	id := c.movedJob().ID
	c.p.Agency.mu.Lock()
	work := c.p.Agency.workerOrigins[id].admission["work"]
	c.p.Agency.mu.Unlock()
	if work != config.WorkInline {
		t.Fatalf("moved job recorded work %q", work)
	}
	close(c.anika.release)
	failed := c.next(func(e events.Event) bool { _, ok := e.(events.JobFailed); return ok }).(events.JobFailed)
	if failed.ID != id || !strings.Contains(failed.Err, "hands none to background jobs") || worker.calls() != 0 {
		t.Fatalf("job failed = %+v, worker ran %d", failed, worker.calls())
	}
}
