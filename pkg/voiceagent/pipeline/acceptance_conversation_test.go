package pipeline

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// This provider holds the first ordinary request after its prompt snapshot.
// The production host must defer a later outcome to the next request rather
// than splice it into a streaming prompt or require a task-specific question.
type acceptanceConversationBrain struct {
	ordinaryResultBrain
	entered     chan struct{}
	release     chan struct{}
	calls       int
	projections [][]brain.CompletionResult
}

func (b *acceptanceConversationBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.calls++
	b.projections = append(b.projections, append([]brain.CompletionResult(nil), opts.WorkResults...))
	if b.calls == 1 {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.ordinaryResultBrain.ThinkStream(ctx, input, opts)
}

func TestAcceptanceUnrelatedConversationBeyondTwoMinutesDefersLateResultOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, c, scope := newCompletionLeasePipeline(t, false)
		if err := c.Dismiss(scope, c.Pending(scope)[0].Outcome.ResultID); err != nil {
			t.Fatal(err)
		}
		b := &acceptanceConversationBrain{entered: make(chan struct{}), release: make(chan struct{})}
		p.Brain = b
		key, err := c.Register(scope, work.Admission{Task: "Denver weather check", Origin: "agency", Provider: "ollama", Model: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Started(key); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- p.RunTurnTextMode(context.Background(), "Tell me about music.") }()
		<-b.entered
		// Virtual monotonic time exercises the real production timeout path; this
		// is deterministic retention evidence, not a wall-clock latency sample.
		time.Sleep(121 * time.Second)
		outcome := work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "Denver: 62 F, clear", SourceRef: "fixture://weather/denver", Limitations: "Recorded fixture; not a live forecast"}
		if _, err = c.Publish(key, outcome); err != nil {
			t.Fatal(err)
		}
		close(b.release)
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) < 2*time.Minute {
			t.Fatal("long conversation boundary not exercised")
		}
		if len(b.projections[0]) != 0 || len(c.Pending(scope)) != 1 {
			t.Fatal("late result changed the in-flight request or was consumed")
		}
		for _, input := range []string{"What instruments are in an orchestra?", "What is a sonata?"} {
			if err = p.RunTurnTextMode(context.Background(), input); err != nil {
				t.Fatal(err)
			}
		}
		s := c.Snapshot(scope)
		if len(s.Pending) != 0 || len(s.Communicated) != 1 {
			t.Fatalf("result disposition: pending=%d communicated=%d", len(s.Pending), len(s.Communicated))
		}
		r := s.Communicated[0]
		if r.Outcome.JobID != key.JobID || r.LastAttempt.Receipt.Mode != "text" || !r.LastAttempt.Receipt.ResponseCommitted {
			t.Fatalf("missing scoped committed receipt: %+v", r)
		}
		if len(b.projections) != 3 || len(b.projections[1]) != 1 || len(b.projections[2]) != 0 {
			t.Fatalf("request snapshots=%+v", b.projections)
		}
		count := 0
		for _, turn := range b.History() {
			if turn.Role == "assistant" && strings.Contains(turn.Content, outcome.Text) {
				count++
			}
		}
		if count != 1 || b.requestCount() != 0 {
			t.Fatalf("canonical update count=%d completion model calls=%d", count, b.requestCount())
		}
		t.Logf("trace: %s admitted -> unrelated request snapshotted -> 121s virtual time -> result published -> current answer committed without result -> next unrelated answer committed with result %s -> next answer has no duplicate", key.JobID, r.Outcome.ResultID)
	})
}
