package pipeline

import (
	"context"
	"errors"
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
	turns []brain.Turn
}

func (b *lateSuccessProjection) ImportHistory(turns []brain.Turn) error {
	b.turns = turns
	return nil
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

func TestStoppedRequestOnASharedHistoryProviderIsNotRecordedByTheHost(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &lateSuccessProjection{lateSuccessBrain: &lateSuccessBrain{}}, Events: events.NewBus(), Transcript: transcript}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "What is the vault code?", brain.StreamOptions{Identity: &brain.TurnIdentity{ResponseID: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	if recording != nil {
		t.Fatal("the host recorded a request to a provider that keeps the shared history")
	}
	<-stream.Chunks
	cancel()
	if result := <-stream.Done; result.Err != nil {
		t.Fatalf("the provider's own result was replaced: %+v", result)
	}
	if turns := transcript.Turns(); len(turns) != 0 {
		t.Fatalf("transcript = %+v", turns)
	}
}
