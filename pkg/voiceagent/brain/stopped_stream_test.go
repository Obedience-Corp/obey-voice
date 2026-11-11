package brain

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

// heldClaudeRunner answers its first request, then holds the stream open
// once it is stopped, until release closes, and closes it without an error:
// the SDK's view of a CLI that had already finished when it was stopped and
// whose output closes late. Later requests answer at once.
type heldClaudeRunner struct {
	fakeClaudeClient
	release chan struct{}
	calls   atomic.Int32
}

func (r *heldClaudeRunner) StreamPrompt(ctx context.Context, _ string, _ *claude.RunOptions) (<-chan claude.Message, <-chan error) {
	msgs, errs := make(chan claude.Message, 2), make(chan error)
	if r.calls.Add(1) > 1 {
		msgs <- assistantMsg("Second answer.")
		msgs <- resultMsg("")
		close(msgs)
		close(errs)
		return msgs, errs
	}
	msgs <- assistantMsg("The vault code is cobalt, and the door is teal.")
	msgs <- resultMsg("")
	go func() {
		<-ctx.Done()
		<-r.release
		close(msgs)
		close(errs)
	}()
	return msgs, errs
}

// heldGrokRunner is heldClaudeRunner for Grok, whose SDK reports nothing once
// its request is stopped.
type heldGrokRunner struct {
	fakeGrokClient
	release chan struct{}
	calls   atomic.Int32
}

func (r *heldGrokRunner) StreamPrompt(ctx context.Context, _ string, _ *grok.RunOptions) (<-chan grok.Event, <-chan error) {
	evs, errs := make(chan grok.Event, 1), make(chan error)
	if r.calls.Add(1) > 1 {
		evs <- grokTextEvent("Second answer.", "")
		close(evs)
		close(errs)
		return evs, errs
	}
	evs <- grokTextEvent("The vault code is cobalt, and the door is teal.", "")
	go func() {
		<-ctx.Done()
		<-r.release
		close(evs)
		close(errs)
	}()
	return evs, errs
}

// A request stopped by its caller may finish on the provider while the next
// request is already running on it. It must leave the provider's history and
// session alone, and report the stop.
func TestStoppedStreamLeavesTheProvidersHistoryAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(release chan struct{}) Provider
	}{
		{name: "claude", start: func(release chan struct{}) Provider {
			return newTestBrain(&heldClaudeRunner{release: release})
		}},
		{name: "grok", start: func(release chan struct{}) Provider {
			return newTestGrokBrain(&heldGrokRunner{release: release})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			p := tc.start(release)
			ctx, stop := context.WithCancel(t.Context())
			first, err := p.ThinkStream(ctx, "What is the vault code?", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			<-first.Chunks
			stop()

			second, err := p.ThinkStream(t.Context(), "Try again.", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if text, result := collectStream(t, second); text != "Second answer." || result.Err != nil {
				t.Fatalf("second request = %q, %+v", text, result)
			}

			close(release)
			for range first.Chunks {
			}
			if result := <-first.Done; !errors.Is(result.Err, context.Canceled) {
				t.Fatalf("the stopped request reported %+v", result)
			}
			history := p.History()
			if len(history) != 3 || history[0].Content != "What is the vault code?" || history[1].Content != "Try again." || history[2].Content != "Second answer." {
				t.Fatalf("history = %+v", history)
			}
		})
	}
}
