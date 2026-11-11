package personaruntime

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// stoppingClaude is the Claude provider reporting a stopped request on its
// own schedule: success at once, or its result only after a delay.
type stoppingClaude struct {
	*brain.Brain
	succeed          bool
	delay            time.Duration
	active, overlaps atomic.Int32
}

func (b *stoppingClaude) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	if b.active.Add(1) > 1 {
		b.overlaps.Add(1)
	}
	stream, err := b.Brain.ThinkStream(ctx, input, opts)
	if err != nil {
		b.active.Add(-1)
		return nil, err
	}
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		var result brain.StreamResult
		select {
		case result = <-stream.Done:
		case <-ctx.Done():
			if b.succeed {
				b.active.Add(-1)
				done <- brain.StreamResult{}
				<-stream.Done
				return
			}
			result = <-stream.Done
			time.Sleep(b.delay)
		}
		b.active.Add(-1)
		done <- result
	}()
	return &brain.Stream{Chunks: stream.Chunks, Done: done}, nil
}

func interruptStall(t *testing.T, c *claudeConversation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
	defer unsubscribe()
	if err := c.p.RunTurnTextMode(turnCtx, "Say more. "+claudeStall); err == nil {
		t.Fatal("interrupted turn reported success")
	}
}

func TestStoppedClaudeRequestStaysInterruptedWhenItReportsSuccessLate(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.p.Brain = &stoppingClaude{Brain: c.p.Brain.(*brain.Brain), succeed: true}
	c.say("Note that the vault code is cobalt.")
	interruptStall(t, c)
	transcript := c.selector.Transcript()
	if partial := transcript[len(transcript)-1]; partial.Disposition != "interrupted" || partial.Content != claudePartial {
		t.Fatalf("stopped reply = %+v", partial)
	}
	if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != 2 {
		t.Fatalf("a stopped reply advanced the cursor: %+v", cursor)
	}
	c.say("Go on.")
	requireContains(t, lastClaudeRequest(t, c.log).Prompt, "Incomplete assistant response", claudePartial)
}

func TestParkedClaudeSessionStillStoppingIsWaitedForBeforeReuse(t *testing.T) {
	c := newClaudeConversation(t, 20)
	nova := testProfile("nova", "Nova", "nova")
	nova.Brain.Provider, nova.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, nova)
	writeTestPrompt(t, c.base.PromptsDir, "nova", "You are {agent_name}.")
	mira := &stoppingClaude{Brain: c.p.Brain.(*brain.Brain), delay: 1500 * time.Millisecond}
	c.p.Brain = mira
	c.say("Note that the vault code is cobalt.")
	interruptStall(t, c)
	// Selecting from the persona list does not wait for Mira's stopped request,
	// so another persona on Claude answers while it is still ending.
	if _, err := c.selector.Select(t.Context(), "nova", "ui"); err != nil {
		t.Fatal(err)
	}
	c.say("What do you think?")
	if _, err := c.selector.Select(t.Context(), "mira", "ui"); err != nil {
		t.Fatal(err)
	}
	if c.p.Brain != mira {
		t.Fatal("returning to Mira did not reuse its parked provider")
	}
	c.say("Go on.")
	if n := mira.overlaps.Load(); n != 0 {
		t.Fatalf("Mira's provider was used while its stopped request was still ending (%d overlaps)", n)
	}
	if got := replies(c.selector.Transcript()); got[len(got)-1].PersonaID != "mira" || got[len(got)-1].Disposition != "" {
		t.Fatalf("replies = %+v", got)
	}
}

// stuckClaude is the Claude provider whose first stopped request does not
// end until release closes; it then sends more text and reports success.
type stuckClaude struct {
	*brain.Brain
	release, ended chan struct{}
	stuck          atomic.Bool
}

func (b *stuckClaude) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	stream, err := b.Brain.ThinkStream(ctx, input, opts)
	if err != nil {
		return nil, err
	}
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		for chunk := range stream.Chunks {
			out <- chunk
		}
		result := <-stream.Done
		late := ctx.Err() != nil && b.stuck.CompareAndSwap(false, true)
		if late {
			<-b.release
			out <- " It is cobalt."
			result = brain.StreamResult{}
		}
		close(out)
		done <- result
		close(done)
		if late {
			close(b.ended)
		}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestAbandonedClaudeRequestIsNeitherRecordedNorSeenLate(t *testing.T) {
	c := newClaudeConversation(t, 20)
	b := &stuckClaude{Brain: c.p.Brain.(*brain.Brain), release: make(chan struct{}), ended: make(chan struct{})}
	c.p.Brain = b
	c.say("Note that the vault code is cobalt.")
	interruptStall(t, c)
	// The stopped request does not end, so the next turn goes ahead without it.
	c.say("Go on.")
	transcript := c.selector.Transcript()
	if len(transcript) != 6 || transcript[3].Content != claudePartial || transcript[3].Disposition != "interrupted" ||
		transcript[4].Content != "Go on." || transcript[5].PersonaID != "mira" || transcript[5].Disposition != "" {
		t.Fatalf("transcript = %+v", transcript)
	}
	cursor := c.selector.ContextSnapshots()["mira"]

	close(b.release)
	<-b.ended
	time.Sleep(300 * time.Millisecond)
	if after := c.selector.Transcript(); !reflect.DeepEqual(after, transcript) {
		t.Fatalf("the abandoned request changed the transcript: %+v", after)
	}
	for _, turn := range transcript {
		if strings.Contains(turn.Content, "It is cobalt") {
			t.Fatalf("late text recorded: %+v", turn)
		}
	}
	if after := c.selector.ContextSnapshots()["mira"]; !reflect.DeepEqual(after, cursor) {
		t.Fatalf("the abandoned request moved Mira's cursor from %+v to %+v", cursor, after)
	}
}
