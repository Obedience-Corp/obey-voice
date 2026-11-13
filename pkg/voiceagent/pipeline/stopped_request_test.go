package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// lateSuccessBrain sends its whole reply, then holds the request until it is
// stopped and reports success, as a CLI may when it is stopped at the end.
type lateSuccessBrain struct{ fakeBrain }

func (b *lateSuccessBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		out <- "The vault code is cobalt."
		<-ctx.Done()
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// lateSuccessProjection is the same provider keeping the shared history.
type lateSuccessProjection struct {
	*lateSuccessBrain
	turns   []brain.Turn
	stopped []brain.StoppedRequest
}

func (b *lateSuccessProjection) ImportHistory(turns []brain.Turn) error {
	b.turns = turns
	return nil
}

func (b *lateSuccessProjection) RecordStoppedRequest(s brain.StoppedRequest) {
	b.stopped = append(b.stopped, s)
}

func TestStoppedRequestReportsTheStopItIsRecordedWith(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &lateSuccessBrain{}, Events: events.NewBus(), Transcript: transcript}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "What is the vault code?", brain.StreamOptions{Identity: &brain.TurnIdentity{ResponseID: "r1", PersonaID: "mira"}})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.Chunks
	cancel()
	result, ok := <-stream.Done
	if !ok || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("a stopped request reached the turn as %+v (ok %v), but is recorded as interrupted", result, ok)
	}
	recording.settle()
	turns := transcript.Turns()
	if len(turns) != 2 || turns[1].Disposition != "interrupted" {
		t.Fatalf("transcript = %+v", turns)
	}
}

// A provider keeping the shared history records its own requests, so the
// host transcript never does. A request the turn stopped reaches the turn as
// stopped, and once it has ended the provider is told what the turn read.
func TestStoppedRequestOnASharedHistoryProviderIsRecordedByTheProviderAsTheTurnReadIt(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &lateSuccessProjection{lateSuccessBrain: &lateSuccessBrain{}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "What is the vault code?", brain.StreamOptions{Identity: &brain.TurnIdentity{ResponseID: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.Chunks
	cancel()
	if result := <-stream.Done; !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("a stopped request reached the turn as %+v", result)
	}
	recording.settle()
	if turns := transcript.Turns(); len(turns) != 0 {
		t.Fatalf("transcript = %+v", turns)
	}
	if len(b.stopped) != 1 {
		t.Fatalf("the provider was told of %d stopped requests", len(b.stopped))
	}
	s := b.stopped[0]
	if s.Line == nil || s.Line.Content != "What is the vault code?" || s.Reply == nil || s.Reply.Content != "The vault code is cobalt." ||
		s.Reply.Disposition != "interrupted" || s.Reply.ResponseID != "r1" || s.Unread != "" {
		t.Fatalf("stopped request = %+v, line %+v, reply %+v", s, s.Line, s.Reply)
	}
}

// A request that completes is left as the provider recorded it.
func TestCompletedRequestOnASharedHistoryProviderIsLeftAsTheProviderRecordedIt(t *testing.T) {
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Kept by the provider."}}}
	recorder := &stoppedRecorder{routedProjectionBrain: b}
	p := &Pipeline{Brain: recorder, Events: events.NewBus()}
	p.AdmitInput = admitAs("response-1", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
		t.Fatal(err)
	}
	if len(recorder.stopped) != 0 {
		t.Fatalf("a completed request was recorded again: %+v", recorder.stopped)
	}
}

type stoppedRecorder struct {
	*routedProjectionBrain
	stopped []brain.StoppedRequest
}

func (b *stoppedRecorder) RecordStoppedRequest(s brain.StoppedRequest) {
	b.stopped = append(b.stopped, s)
}

// routeAfterStopProjection is routeAfterStopBrain keeping the shared history.
type routeAfterStopProjection struct {
	*routeAfterStopBrain
	stopped []brain.StoppedRequest
}

func (b *routeAfterStopProjection) ImportHistory([]brain.Turn) error { return nil }

func (b *routeAfterStopProjection) RecordStoppedRequest(s brain.StoppedRequest) {
	b.stopped = append(b.stopped, s)
}

// A provider keeping the shared history that chose a route the stopped turn
// never followed is told so before the turn returns, with the user's line.
func TestStoppedRouteOnASharedHistoryProviderGivesItTheLineBack(t *testing.T) {
	b := &routeAfterStopProjection{routeAfterStopBrain: &routeAfterStopBrain{partial: "I'll start "}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = admitAs("routed", func(input string) string { return input })
	var routed []RouteDecision
	p.OnRoute = func(d RouteDecision) { routed = append(routed, d) }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	if err := p.RunTurnTextMode(ctx, "Start a worker on the report."); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(routed) != 0 || len(b.stopped) != 1 {
		t.Fatalf("routed = %+v, stopped = %+v", routed, b.stopped)
	}
	if s := b.stopped[0]; s.Line == nil || s.Line.Content != "Start a worker on the report." || s.Reply == nil || s.Reply.Content != "I'll start" || s.Reply.Disposition != "interrupted" {
		t.Fatalf("stopped request = %+v", s)
	}
}

// lateThinkingProjection keeps the shared history and, as Ollama can, sends
// thinking after its last text and closes its result before its thinking.
type lateThinkingProjection struct{ routedProjectionBrain }

func (b *lateThinkingProjection) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	out, thinking, done := make(chan string, 8), make(chan string, 8), make(chan brain.StreamResult, 1)
	out <- "Done."
	for _, t := range []string{"checking ", "the ", "notes"} {
		thinking <- t
	}
	done <- brain.StreamResult{}
	close(done)
	close(thinking)
	close(out)
	return &brain.Stream{Chunks: out, Thinking: thinking, Done: done}, nil
}

func TestThinkingAfterTheLastTextOfASharedHistoryProviderReachesTheTurn(t *testing.T) {
	for range 100 {
		p := &Pipeline{Brain: &lateThinkingProjection{routedProjectionBrain{fakeBrain: &fakeBrain{}}}, Events: events.NewBus()}
		p.AdmitInput = admitAs("thinking", func(input string) string { return input })
		var mu sync.Mutex
		var thought strings.Builder
		events.Subscribe(p.Events, func(e events.ThinkingDelta) {
			mu.Lock()
			thought.WriteString(e.Text)
			mu.Unlock()
		})
		if err := p.RunTurnTextMode(t.Context(), "What do the notes say?"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		got := thought.String()
		mu.Unlock()
		if got != "checking the notes" {
			t.Fatalf("thinking = %q", got)
		}
	}
}
