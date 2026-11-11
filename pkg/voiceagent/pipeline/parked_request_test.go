package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// interruptFirstTurn stops the first reply as soon as it starts streaming.
func interruptFirstTurn(t *testing.T, p *Pipeline) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	defer unsubscribe()
	p.AdmitInput = admitAs("first", func(input string) string { return input })
	_ = p.RunTurnTextMode(ctx, "What is the vault code?")
}

func TestStoppedRequestEndsBeforeItsParkedProviderIsUsedAgain(t *testing.T) {
	transcript := &recordedTranscript{}
	parked := &slowStopBrain{stop: 500 * time.Millisecond}
	p := &Pipeline{Brain: parked, Events: events.NewBus(), Transcript: transcript}
	if err := p.BeginConversationIdentity("parked"); err != nil {
		t.Fatal(err)
	}
	interruptFirstTurn(t, p)

	// Another persona answers while the stopped request is still ending, then
	// the first persona is selected again.
	p.Brain = &fakeBrain{chunks: []string{"Another answer."}}
	p.AdmitInput = admitAs("other", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Anyone else?"); err != nil {
		t.Fatal(err)
	}
	p.Brain = parked
	p.AdmitInput = admitAs("second", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Try again."); err != nil {
		t.Fatal(err)
	}
	if n := parked.overlaps.Load(); n != 0 {
		t.Fatalf("the parked provider was used while its stopped request was still ending (%d overlaps)", n)
	}
	turns := transcript.Turns()
	if len(turns) != 6 || turns[1].Disposition != "interrupted" || turns[3].Content != "Another answer." || turns[5].Content != "Second answer." {
		t.Fatalf("transcript = %+v", turns)
	}
}

type closingSlowStopBrain struct {
	*slowStopBrain
	closed, early atomic.Bool
}

func (b *closingSlowStopBrain) Close() error {
	if b.active.Load() > 0 {
		b.early.Store(true)
	}
	b.closed.Store(true)
	return nil
}

func TestRetiredProviderClosesAfterItsStoppedRequestEnds(t *testing.T) {
	b := &closingSlowStopBrain{slowStopBrain: &slowStopBrain{stop: 500 * time.Millisecond}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: &recordedTranscript{}}
	if err := p.BeginConversationIdentity("retired"); err != nil {
		t.Fatal(err)
	}
	interruptFirstTurn(t, p)
	p.Brain = &fakeBrain{}
	p.RetireBrain(b)
	if err := p.CloseBrains(); err != nil {
		t.Fatal(err)
	}
	if !b.closed.Load() || b.early.Load() {
		t.Fatalf("closed = %v, closed while its request was still ending = %v", b.closed.Load(), b.early.Load())
	}
}
