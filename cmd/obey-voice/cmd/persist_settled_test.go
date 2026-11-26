package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type neverEnding struct{}

func (neverEnding) AwaitSharedHistory(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestSaveSkipsWhileAStoppedRequestIsStillEnding(t *testing.T) {
	saved := persistSettleTimeout
	persistSettleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { persistSettleTimeout = saved })
	var warn bytes.Buffer
	calls := 0
	started := time.Now()
	saveSettled(neverEnding{}, "session-a", func() error { calls++; return nil }, &warn)
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the save waited %v", took)
	}
	if calls != 0 {
		t.Fatalf("saved %d times while a request was still ending", calls)
	}
	if !strings.Contains(warn.String(), "session session-a not saved now, a stopped request is still ending") {
		t.Fatalf("warning = %q", warn.String())
	}
}

// sharedHistoryBrain keeps the shared history and records its reply in full
// as it starts, as a live provider does. Stopped, it sends the rest of the
// reply and ends a little later; the host's record of the stop then trims
// what the user never received.
type sharedHistoryBrain struct {
	endAfter time.Duration
	mu       sync.Mutex
	turns    []brain.Turn
}

func (b *sharedHistoryBrain) ThinkStream(ctx context.Context, input string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.turns = append(b.turns, brain.Turn{Role: "user", Content: input}, brain.Turn{Role: "assistant", Content: "The gate code is 4512."})
	b.mu.Unlock()
	out, done := make(chan string), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		defer close(out)
		out <- "The gate code is"
		<-ctx.Done()
		out <- " 4512."
		time.Sleep(b.endAfter)
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *sharedHistoryBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", errors.New("not used")
}

func (b *sharedHistoryBrain) ClearHistory() { b.LoadHistory(nil) }

func (b *sharedHistoryBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.turns)
}

func (b *sharedHistoryBrain) LoadHistory(turns []brain.Turn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = slices.Clone(turns)
}

func (b *sharedHistoryBrain) ImportHistory(turns []brain.Turn) error {
	b.LoadHistory(turns)
	return nil
}

func (b *sharedHistoryBrain) RecordStoppedRequest(s brain.StoppedRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	last := &b.turns[len(b.turns)-1]
	last.Content = strings.TrimSpace(strings.TrimSuffix(last.Content, strings.TrimSpace(s.Unread)))
	last.Disposition = "interrupted"
}

// Saving right after a turn is stopped, as quitting does, saves the history
// the stopped request corrected: not the reply text the user never received.
func TestSaveAfterAStoppedTurnKeepsOnlyWhatTheUserReceived(t *testing.T) {
	b := &sharedHistoryBrain{endAfter: 300 * time.Millisecond}
	p := &pipeline.Pipeline{Brain: b, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("session-b"); err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (pipeline.ResponseAdmission, error) {
		return pipeline.ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{SessionID: "session-b", ResponseID: "r1", PersonaID: "mira", DisplayName: "Mira"}}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	defer unsubscribe()
	turn := make(chan error, 1)
	go func() { turn <- p.RunTurnTextMode(ctx, "What is the gate code?") }()
	<-ctx.Done()

	var saved []brain.Turn
	saveSettled(p, "session-b", func() error { saved = b.History(); return nil }, io.Discard)
	if err := <-turn; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped turn returned %v", err)
	}
	if len(saved) != 2 || saved[1].Content != "The gate code is" || saved[1].Disposition != "interrupted" {
		t.Fatalf("saved %+v, want the reply as the user received it", saved)
	}
}
