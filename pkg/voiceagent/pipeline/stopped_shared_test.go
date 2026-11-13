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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// endingProjection keeps the shared history. A line with "HOLD" in it gets
// part of a reply, and its request ends once it is stopped and then release
// closes, or stopAfter later without release. Any other line is answered.
type endingProjection struct {
	fakeBrain
	release   chan struct{}
	stopAfter time.Duration

	mu        sync.Mutex
	turns     []brain.Turn
	stopped   []brain.StoppedRequest
	abandoned int
}

func (b *endingProjection) ThinkStream(ctx context.Context, input string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		defer close(out)
		if !strings.Contains(input, "HOLD") {
			out <- "Noted."
			b.mu.Lock()
			b.turns = append(b.turns, brain.Turn{Role: "user", Content: input}, brain.Turn{Role: "assistant", Content: "Noted."})
			b.mu.Unlock()
			done <- brain.StreamResult{}
			return
		}
		out <- "The vault code is"
		<-ctx.Done()
		if b.release != nil {
			<-b.release
		} else {
			time.Sleep(b.stopAfter)
		}
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *endingProjection) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.turns)
}

func (b *endingProjection) ClearHistory() { b.LoadHistory(nil) }

func (b *endingProjection) LoadHistory(turns []brain.Turn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = slices.Clone(turns)
}

func (b *endingProjection) ImportHistory(turns []brain.Turn) error {
	b.LoadHistory(turns)
	return nil
}

func (b *endingProjection) RecordStoppedRequest(s brain.StoppedRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, s)
	for _, t := range []*brain.Turn{s.Line, s.Reply} {
		if t != nil {
			b.turns = append(b.turns, *t)
		}
	}
}

func (b *endingProjection) AbandonRequest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.abandoned++
}

func (b *endingProjection) records() ([]brain.StoppedRequest, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.stopped), b.abandoned
}

func newEndingPipeline(t *testing.T, b *endingProjection) *Pipeline {
	t.Helper()
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("s"); err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = admitAs("r", func(input string) string { return input })
	return p
}

// stopHeldTurn runs a turn on a held line and stops it once its reply starts.
func stopHeldTurn(t *testing.T, p *Pipeline) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	defer unsubscribe()
	started := time.Now()
	if err := p.RunTurnTextMode(ctx, "HOLD Say more."); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped turn returned %v", err)
	}
	return time.Since(started)
}

func lastTurnMetrics(p *Pipeline) func() events.TurnMetrics {
	var mu sync.Mutex
	var last events.TurnMetrics
	events.Subscribe(p.Events, func(e events.TurnMetrics) {
		mu.Lock()
		defer mu.Unlock()
		last = e
	})
	return func() events.TurnMetrics {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// A stopped request on a provider keeping the shared history that ends after
// the conversation was cleared is not recorded in the new conversation.
func TestSharedHistoryRequestEndingAfterAClearIsNotRecorded(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 50*time.Millisecond)
	b := &endingProjection{release: make(chan struct{})}
	p := newEndingPipeline(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "HOLD Say more.", brain.StreamOptions{Identity: &brain.TurnIdentity{ResponseID: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.Chunks
	cancel()
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	<-recording.drained
	recording.settle()
	if stopped, _ := b.records(); len(stopped) != 0 || len(b.History()) != 0 {
		t.Fatalf("the cleared conversation got the stopped request back: %+v, history %+v", stopped, b.History())
	}
}

// A stopped request on a provider keeping the shared history that never ends
// is given up on once the stop wait is over: the stopped turn returns, and a
// new turn, a persona switch and a clear go ahead without waiting again.
// Nothing it does when it finally ends is recorded.
func TestSharedHistoryRequestThatNeverEndsHoldsNothingUp(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 500*time.Millisecond)
	b := &endingProjection{release: make(chan struct{})}
	p := newEndingPipeline(t, b)
	metrics := lastTurnMetrics(p)

	if took := stopHeldTurn(t, p); took > 5*time.Second {
		t.Fatalf("the stopped turn took %v to return", took)
	}
	if wait := metrics().StopWaitElapsed; wait < providerStopTimeout {
		t.Fatalf("stop wait = %v, want the whole %v", wait, providerStopTimeout)
	}
	if _, abandoned := b.records(); abandoned != 1 {
		t.Fatalf("the provider was told of %d abandoned requests", abandoned)
	}
	pending := p.requests[len(p.requests)-1]

	quick := func(name string, run func() error) {
		t.Helper()
		started := time.Now()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if took := time.Since(started); took >= providerStopTimeout {
			t.Fatalf("%s waited %v for the abandoned request", name, took)
		}
	}
	quick("new turn", func() error { return p.RunTurnTextMode(t.Context(), "Go on.") })
	quick("persona switch", func() error { return p.AwaitSharedHistory(t.Context()) })
	quick("clear", p.ClearConversation)

	close(b.release)
	<-pending.drained
	if stopped, _ := b.records(); len(stopped) != 0 || len(b.History()) != 0 {
		t.Fatalf("the abandoned request was recorded when it ended: %+v, history %+v", stopped, b.History())
	}
}

// A switch that waits for a stopped request while its turn is still waiting
// for it goes ahead as soon as the turn gives up, not a whole wait later.
func TestSwitchWaitingWithAStoppedTurnGoesAheadWithIt(t *testing.T) {
	SetProviderStopTimeoutForTest(t, time.Minute)
	b := &endingProjection{release: make(chan struct{})}
	defer close(b.release)
	p := newEndingPipeline(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "HOLD Say more.", brain.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.Chunks
	cancel()
	waited := make(chan error, 1)
	go func() { waited <- p.AwaitSharedHistory(t.Context()) }()
	select {
	case err := <-waited:
		t.Fatalf("the switch did not wait for the stopped request: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	recording.abandon()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the switch kept waiting for an abandoned request")
	}
}

// The stopped turn waits for its request to end and reports how long that
// took; a turn that was not stopped reports no wait.
func TestStoppedTurnReportsHowLongItsProviderTookToStop(t *testing.T) {
	b := &endingProjection{stopAfter: 150 * time.Millisecond}
	p := newEndingPipeline(t, b)
	metrics := lastTurnMetrics(p)
	stopHeldTurn(t, p)
	// The provider's clock starts at the stop, a little before the turn's.
	if wait := metrics().StopWaitElapsed; wait < b.stopAfter/2 || wait >= providerStopTimeout {
		t.Fatalf("stop wait = %v, want about %v", wait, b.stopAfter)
	}
	if stopped, abandoned := b.records(); len(stopped) != 1 || abandoned != 0 {
		t.Fatalf("stopped = %+v, abandoned %d", stopped, abandoned)
	}
	if err := p.RunTurnTextMode(t.Context(), "Go on."); err != nil {
		t.Fatal(err)
	}
	if wait := metrics().StopWaitElapsed; wait != 0 {
		t.Fatalf("a turn that was not stopped reported a stop wait of %v", wait)
	}
}
