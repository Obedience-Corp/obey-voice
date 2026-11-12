package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// lateStopBrain answers its first request in part and keeps it open once it
// is stopped: until release closes or, without release, for stop. It then
// sends more text and reports success, as a CLI may on its way out. Later
// requests answer after answerAfter.
type lateStopBrain struct {
	fakeBrain
	stop                    time.Duration
	release                 chan struct{}
	answerAfter             time.Duration
	history                 []brain.Turn
	calls, active, overlaps atomic.Int32
}

func (b *lateStopBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	if b.active.Add(1) > 1 {
		b.overlaps.Add(1)
	}
	first := b.calls.Add(1) == 1
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		defer close(out)
		defer b.active.Add(-1)
		if !first {
			time.Sleep(b.answerAfter)
			out <- "Second answer."
			done <- brain.StreamResult{}
			return
		}
		out <- "The vault code is"
		<-ctx.Done()
		if b.release != nil {
			<-b.release
		} else {
			time.Sleep(b.stop)
		}
		out <- " cobalt."
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *lateStopBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "Summary.", nil
}

func (b *lateStopBrain) History() []brain.Turn { return b.history }

func (b *lateStopBrain) AppendAssistantResponseWithIdentity(string, brain.TurnIdentity) error {
	return nil
}

// serialStopBrain is a lateStopBrain that cannot take a request while an
// earlier one is still ending.
type serialStopBrain struct{ lateStopBrain }

func (*serialStopBrain) SerialRequests() {}

// stopFirstReply runs a turn, stops it as its reply starts, and returns the
// request it stopped.
func stopFirstReply(t *testing.T, p *Pipeline) *transcriptRecording {
	t.Helper()
	p.AdmitInput = admitAs("first", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	defer unsubscribe()
	if err := p.RunTurnTextMode(ctx, "What is the vault code?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped turn returned %v", err)
	}
	pending := p.stillEnding(p.Brain)
	if len(pending) != 1 {
		t.Fatalf("%d stopped requests still ending", len(pending))
	}
	return pending[0]
}

// statusLines collects the errors and status lines p reports from now on.
func statusLines(p *Pipeline) func() []string {
	var mu sync.Mutex
	var lines []string
	add := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	}
	events.Subscribe(p.Events, func(e events.Error) { add("error: " + e.Message) })
	events.Subscribe(p.Events, func(e events.Info) { add("info: " + e.Message) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

// askAgain runs the next turn, which must answer without failing.
func askAgain(t *testing.T, p *Pipeline) time.Duration {
	t.Helper()
	p.AdmitInput = admitAs("second", func(input string) string { return input })
	var mu sync.Mutex
	var replies []string
	unsubscribe := events.SubscribeDetachable(p.Events, func(e events.ResponseReady) {
		mu.Lock()
		defer mu.Unlock()
		replies = append(replies, e.Response)
	})
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := p.RunTurnTextMode(ctx, "Try again."); err != nil {
		t.Fatalf("the new turn failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(replies) != 1 || replies[0] != "Second answer." {
		t.Fatalf("the new turn's replies = %q", replies)
	}
	return time.Since(started)
}

// requireStoppedThenAnswered checks the stopped reply is kept as heard and
// the new exchange follows it, each recorded once.
func requireStoppedThenAnswered(t *testing.T, transcript *recordedTranscript, tail ...brain.Turn) {
	t.Helper()
	turns := transcript.Turns()
	want := append([]brain.Turn{
		{Role: "user", Content: "What is the vault code?"},
		{Role: "assistant", Content: "The vault code is", Disposition: "interrupted", ResponseID: "first"},
	}, tail...)
	if len(turns) != len(want) {
		t.Fatalf("transcript = %+v", turns)
	}
	for i, w := range want {
		got := turns[i]
		if got.Role != w.Role || got.Content != w.Content || got.Disposition != w.Disposition || got.ResponseID != w.ResponseID {
			t.Fatalf("turn %d = %+v, want %+v; transcript = %+v", i, got, w, turns)
		}
	}
}

var newExchange = []brain.Turn{
	{Role: "user", Content: "Try again."},
	{Role: "assistant", Content: "Second answer.", ResponseID: "second"},
}

func TestHostTranscriptNewTurnGoesAheadOfARequestThatNeverStops(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 100*time.Millisecond)
	transcript := &recordedTranscript{}
	b := &lateStopBrain{release: make(chan struct{})}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	stopped := stopFirstReply(t, p)
	status := statusLines(p)

	askAgain(t, p)
	requireStoppedThenAnswered(t, transcript, newExchange...)

	close(b.release)
	<-stopped.drained
	requireStoppedThenAnswered(t, transcript, newExchange...)
	if lines := status(); len(lines) != 0 {
		t.Fatalf("status = %q", lines)
	}
}

func TestHostTranscriptNewTurnAfterARequestThatTakesASecondToStop(t *testing.T) {
	t.Run("within the wait", func(t *testing.T) {
		transcript := &recordedTranscript{}
		b := &lateStopBrain{stop: time.Second}
		p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
		stopFirstReply(t, p)
		status := statusLines(p)
		askAgain(t, p)
		requireStoppedThenAnswered(t, transcript, newExchange...)
		if lines := status(); len(lines) != 0 {
			t.Fatalf("status = %q", lines)
		}
		if n := b.overlaps.Load(); n != 0 {
			t.Fatalf("the new request started while the stopped one was still ending (%d overlaps)", n)
		}
	})
	t.Run("past the wait, stopping while the new turn answers", func(t *testing.T) {
		SetProviderStopTimeoutForTest(t, 100*time.Millisecond)
		transcript := &recordedTranscript{}
		b := &lateStopBrain{stop: time.Second, answerAfter: 1500 * time.Millisecond}
		p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
		stopped := stopFirstReply(t, p)
		status := statusLines(p)
		askAgain(t, p)
		<-stopped.drained
		requireStoppedThenAnswered(t, transcript, newExchange...)
		if lines := status(); len(lines) != 0 {
			t.Fatalf("status = %q", lines)
		}
	})
}

func TestHostTranscriptNewTurnWaitsQuietlyForAProviderThatCannotOverlap(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 100*time.Millisecond)
	transcript := &recordedTranscript{}
	b := &serialStopBrain{lateStopBrain{stop: 600 * time.Millisecond}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	stopFirstReply(t, p)
	status := statusLines(p)
	askAgain(t, p)
	if n := b.overlaps.Load(); n != 0 {
		t.Fatalf("the provider got a request while the stopped one was still ending (%d overlaps)", n)
	}
	if lines := status(); len(lines) != 1 || lines[0] != "info: "+providerStoppingInfo {
		t.Fatalf("status = %q", lines)
	}
	requireStoppedThenAnswered(t, transcript, newExchange...)
}

func TestBackgroundTurnAndCompactionGoAheadOfARequestThatNeverStops(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 100*time.Millisecond)
	t.Run("background turn", func(t *testing.T) {
		transcript := &recordedTranscript{}
		b := &lateStopBrain{release: make(chan struct{})}
		defer close(b.release)
		p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
		if err := p.BeginConversationIdentity("background"); err != nil {
			t.Fatal(err)
		}
		jobs := job.NewTable(t.Context())
		t.Cleanup(jobs.Close)
		NewAgency(p, jobs, nil, &config.Config{}, nil).Wire()
		stopFirstReply(t, p)
		status := statusLines(p)
		p.Agency.mu.Lock()
		p.Agency.workerReplies = append(p.Agency.workerReplies, workerReply{workerOrigin: workerOrigin{Envelope: events.ResponseEnvelope{PersonaID: "mira", ResponseID: "worker"}}, Text: "The report is ready."})
		p.Agency.mu.Unlock()
		if err := p.RunBackgroundTurn(t.Context()); err != nil {
			t.Fatalf("background turn failed: %v", err)
		}
		requireStoppedThenAnswered(t, transcript, brain.Turn{Role: "assistant", Content: "The report is ready.", ResponseID: "worker"})
		if lines := status(); len(lines) != 0 {
			t.Fatalf("status = %q", lines)
		}
	})
	t.Run("compaction", func(t *testing.T) {
		b := &lateStopBrain{release: make(chan struct{}), history: []brain.Turn{{Role: "user", Content: "Hello."}, {Role: "assistant", Content: "Hi."}}}
		defer close(b.release)
		p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: &recordedTranscript{}, CompactPrompt: "Summarize."}
		stopFirstReply(t, p)
		if err := p.CompactConversation(t.Context()); err != nil {
			t.Fatalf("compaction failed: %v", err)
		}
	})
}
